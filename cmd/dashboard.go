package cmd

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	brandassets "github.com/labring/sealtun/assets"
	"github.com/labring/sealtun/pkg/auth"
	"github.com/labring/sealtun/pkg/k8s"
	"github.com/labring/sealtun/pkg/publicauth"
	"github.com/labring/sealtun/pkg/session"
	"github.com/spf13/cobra"
)

type dashboardPayload struct {
	GeneratedAt string               `json:"generatedAt"`
	Status      *statusPayload       `json:"status,omitempty"`
	Tunnels     []listItem           `json:"tunnels,omitempty"`
	Doctor      *doctorPayload       `json:"doctor,omitempty"`
	Domains     *domainStatusPayload `json:"domains,omitempty"`
	Warnings    []string             `json:"warnings,omitempty"`
}

type dashboardContextPayload struct {
	Regions  []regionListItem `json:"regions,omitempty"`
	Warnings []string         `json:"warnings,omitempty"`
}

type dashboardPageData struct {
	Token  string
	Remote bool
}

type dashboardServer struct {
	token         string
	embedToken    bool
	pageBasicAuth *publicauth.BasicAuth
}

type dashboardScope struct {
	region    string
	namespace string
	client    *k8s.Client
}

var dashboardAddr string
var dashboardPort int
var dashboardAllowRemote bool
var dashboardBasicAuth string
var dashboardBasicAuthUser string
var dashboardBasicAuthPassword string
var dashboardBasicAuthPasswordEnv string
var dashboardOpen bool

var dashboardCmd = &cobra.Command{
	Use:          "dashboard",
	Short:        "Run a local Sealtun dashboard",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if dashboardPort < 1 || dashboardPort > 65535 {
			return fmt.Errorf("invalid dashboard port %d", dashboardPort)
		}
		pageBasicAuth, err := dashboardBasicAuthConfig()
		if err != nil {
			return err
		}
		return runDashboard(cmd.Context(), dashboardAddr, dashboardPort, dashboardAllowRemote, pageBasicAuth)
	},
}

func init() {
	rootCmd.AddCommand(dashboardCmd)
	dashboardCmd.Flags().StringVar(&dashboardAddr, "addr", "127.0.0.1", "Dashboard listen address")
	dashboardCmd.Flags().IntVar(&dashboardPort, "port", 19777, "Dashboard listen port")
	dashboardCmd.Flags().BoolVar(&dashboardAllowRemote, "allow-remote", false, "Allow dashboard to listen on a non-loopback address")
	dashboardCmd.Flags().StringVar(&dashboardBasicAuth, "basic-auth", "", "Protect dashboard pages and APIs with Basic Auth using username:password")
	dashboardCmd.Flags().StringVar(&dashboardBasicAuthUser, "basic-auth-user", "", "Dashboard Basic Auth username")
	dashboardCmd.Flags().StringVar(&dashboardBasicAuthPassword, "basic-auth-password", "", "Dashboard Basic Auth password")
	dashboardCmd.Flags().StringVar(&dashboardBasicAuthPasswordEnv, "basic-auth-password-env", "", "Read dashboard Basic Auth password from an environment variable")
	dashboardCmd.Flags().BoolVar(&dashboardOpen, "open", false, "Open the dashboard URL in the browser")
}

func dashboardBasicAuthConfig() (*publicauth.BasicAuth, error) {
	if dashboardBasicAuth == "" && dashboardBasicAuthUser == "" && dashboardBasicAuthPassword == "" && dashboardBasicAuthPasswordEnv == "" {
		return nil, nil
	}
	warnPlaintextPasswordFlag(dashboardBasicAuth, dashboardBasicAuthPassword)
	basicAuth, err := resolveBasicAuth(basicAuthInput{
		Credential:  dashboardBasicAuth,
		Username:    dashboardBasicAuthUser,
		Password:    dashboardBasicAuthPassword,
		PasswordEnv: dashboardBasicAuthPasswordEnv,
	}, getenv)
	if err != nil {
		return nil, fmt.Errorf("dashboard basic auth: %w", err)
	}
	return &publicauth.BasicAuth{
		Username:     basicAuth.Username,
		PasswordHash: basicAuthPasswordHash(basicAuth),
	}, nil
}

func runDashboard(ctx context.Context, addr string, port int, allowRemote bool, pageBasicAuth *publicauth.BasicAuth) error {
	if pageBasicAuth != nil {
		if err := publicauth.Validate(*pageBasicAuth); err != nil {
			return fmt.Errorf("dashboard basic auth: %w", err)
		}
	}
	loopback, err := validateDashboardListen(addr, allowRemote)
	if err != nil {
		return err
	}
	// In remote mode the page-fragment token is the only credential unless
	// Basic Auth is configured, and that token is comparatively easy to leak
	// (process args, shell history, logs). Require Basic Auth so a leaked or
	// brute-forced token alone cannot grant write access over the network.
	if !loopback && pageBasicAuth == nil {
		return fmt.Errorf("refusing to expose dashboard remotely without Basic Auth; pass --basic-auth-user and --basic-auth-password-env (the token alone is not sufficient for network-exposed access)")
	}

	mux := http.NewServeMux()
	token, err := newDashboardToken()
	if err != nil {
		return err
	}
	handler := dashboardServer{
		token:         token,
		embedToken:    loopback,
		pageBasicAuth: pageBasicAuth,
	}
	mux.HandleFunc("/", handler.serveHome)
	mux.HandleFunc("/favicon.svg", serveDashboardFavicon)
	mux.HandleFunc("/logo.svg", serveDashboardFavicon)
	mux.HandleFunc("/api/summary", handler.serveDashboardSummary)
	mux.HandleFunc("/api/context", handler.serveContext)
	mux.HandleFunc("/api/", handler.serveAPI)

	server := &http.Server{
		Addr:              net.JoinHostPort(addr, strconv.Itoa(port)),
		Handler:           handler.withPageAuth(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// A global write timeout protects every handler from slow-write
		// (Slowloris-style) resource exhaustion. The long-lived SSE watch
		// handler clears this deadline per-connection via ResponseController.
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() {
		displayURL := fmt.Sprintf("http://%s", server.Addr)
		if loopback {
			fmt.Printf("Sealtun dashboard listening on %s\n", displayURL)
		} else {
			displayURL = fmt.Sprintf("%s/#token=%s", displayURL, token)
			// Print the tokenized URL to stderr (not stdout) so it is not
			// captured by output redirection/pipes, and is less likely to end
			// up in logs. The token must be included here, otherwise a remote
			// user who does not pass --open has no way to obtain it (the HTML
			// does not embed the token in remote mode). Basic Auth is also
			// required in this mode.
			fmt.Fprintf(os.Stderr, "Sealtun dashboard listening on %s\n", displayURL)
			fmt.Fprintln(os.Stderr, "Remote dashboard access requires the token in the URL fragment and Basic Auth; keep the URL private.")
		}
		if pageBasicAuth != nil {
			fmt.Println("Dashboard Basic Auth is enabled.")
		}
		if dashboardOpen {
			openBrowser(displayURL)
		}
		errc <- server.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errc:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s dashboardServer) withPageAuth(next http.Handler) http.Handler {
	if s.pageBasicAuth == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || !publicauth.Check(*s.pageBasicAuth, username, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="Sealtun Dashboard"`)
			http.Error(w, "dashboard authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s dashboardServer) serveHome(w http.ResponseWriter, r *http.Request) {
	if !requireDashboardMethod(w, r, http.MethodGet) {
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageToken := ""
	if s.embedToken {
		pageToken = s.token
	}
	_ = dashboardHTML.Execute(w, dashboardPageData{
		Token:  pageToken,
		Remote: !s.embedToken,
	})
}

func (s dashboardServer) serveDashboardSummary(w http.ResponseWriter, r *http.Request) {
	if !requireDashboardMethod(w, r, http.MethodGet) {
		return
	}
	if !s.requireToken(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	payload := collectDashboardPayload(r.Context())
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

func serveDashboardFavicon(w http.ResponseWriter, r *http.Request) {
	if !requireDashboardMethod(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write(brandassets.SealtunLogoSVG)
}

func (s dashboardServer) serveContext(w http.ResponseWriter, r *http.Request) {
	if !requireDashboardMethod(w, r, http.MethodGet) {
		return
	}
	if !s.requireToken(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	payload := dashboardContextPayload{}
	if root, err := auth.CurrentSealtunDir(); err != nil {
		payload.Warnings = append(payload.Warnings, fmt.Sprintf("regions unavailable: active config directory could not be loaded: %v", err))
	} else if authData, err := auth.LoadAuthDataFromDir(root); err == nil {
		payload.Regions = dashboardRegionItemsForAuth(authData)
	} else if !os.IsNotExist(err) {
		payload.Warnings = append(payload.Warnings, fmt.Sprintf("regions unavailable: active auth data could not be loaded: %v", err))
	}
	payload.Warnings = append(payload.Warnings, "dashboard is scoped to the active login only; use `sealtun profile use <name>` in the CLI to switch profiles")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

func requireDashboardMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func (s dashboardServer) requireToken(w http.ResponseWriter, r *http.Request) bool {
	got := r.Header.Get("X-Sealtun-Dashboard-Token")
	if got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1 {
		return true
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeDashboardError(w, http.StatusForbidden, "invalid dashboard token")
		return false
	}
	http.Error(w, "invalid dashboard token", http.StatusForbidden)
	return false
}

func collectDashboardPayload(ctx context.Context) dashboardPayload {
	payload := dashboardPayload{
		GeneratedAt: time.Now().Format(time.RFC3339),
	}
	configDir, configErr := auth.CurrentSealtunDir()
	if configErr != nil {
		payload.Warnings = append(payload.Warnings, fmt.Sprintf("dashboard scope unavailable: active config directory could not be loaded: %v", configErr))
	}
	if configErr == nil {
		status, err := collectStatusFromDir(configDir)
		if err != nil {
			payload.Warnings = append(payload.Warnings, fmt.Sprintf("status unavailable: %v", err))
		} else {
			payload.Status = status
		}
	}

	scope, scopeWarnings := dashboardScopeFromStatus(configDir, payload.Status)
	payload.Warnings = append(payload.Warnings, scopeWarnings...)

	scopedSessions, sessionWarnings, err := dashboardScopedSessions(configDir, scope)
	if err != nil {
		payload.Warnings = append(payload.Warnings, fmt.Sprintf("tunnels unavailable: %v", err))
	} else {
		payload.Warnings = append(payload.Warnings, sessionWarnings...)
		tunnels := listItemsFromSessions(scopedSessions, true)
		var hostWarnings []string
		tunnels, hostWarnings = sanitizeDashboardTunnelHosts(tunnels)
		payload.Warnings = append(payload.Warnings, hostWarnings...)
		payload.Tunnels = tunnels
		if payload.Status != nil {
			doctor, err := collectDoctorPayloadFromItems(ctx, payload.Status, tunnels, dashboardRemoteDiagnostics(scope))
			if err != nil {
				payload.Warnings = append(payload.Warnings, fmt.Sprintf("doctor unavailable: %v", err))
			} else {
				payload.Doctor = doctor
			}
		}
		payload.Domains = collectDomainStatusFromSessions(ctx, scopedSessions, 8*time.Second, func(ctx context.Context, sess session.TunnelSession) *domainVerifyPayload {
			return verifyDomainForSessionWithRemote(ctx, sess, dashboardRemoteDiagnostics(scope))
		})
	}
	return payload
}

func sanitizeDashboardTunnelHosts(items []listItem) ([]listItem, []string) {
	warnings := []string{}
	sanitized := make([]listItem, len(items))
	copy(sanitized, items)
	for i := range sanitized {
		sanitizeHostField := func(label string, value *string) {
			if *value == "" || *value == "-" {
				return
			}
			host, err := normalizePublicHostname(*value)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("dashboard hid invalid %s for tunnel %s: %v", label, sanitized[i].TunnelID, err))
				*value = ""
				return
			}
			*value = host
		}
		sanitizeHostField("public host", &sanitized[i].Host)
		sanitizeHostField("Sealos host", &sanitized[i].SealosHost)
		sanitizeHostField("custom domain", &sanitized[i].CustomDomain)
	}
	return sanitized, warnings
}

func dashboardRegionItemsForAuth(authData *auth.AuthData) []regionListItem {
	if authData == nil || authData.Region == "" {
		return nil
	}
	for _, item := range regionListItemsForCurrent(authData.Region) {
		if item.Current {
			if authData.SealosDomain != "" {
				item.SealosDomain = authData.SealosDomain
			}
			return []regionListItem{item}
		}
	}
	return []regionListItem{{
		Name:         authData.Region,
		URL:          authData.Region,
		SealosDomain: authData.SealosDomain,
		Current:      true,
	}}
}

func dashboardScopeFromStatus(configDir string, status *statusPayload) (*dashboardScope, []string) {
	if status == nil {
		return nil, []string{"dashboard scope unavailable: active status could not be loaded"}
	}
	if configDir == "" {
		return nil, []string{"dashboard scope unavailable: active config directory is unavailable"}
	}
	if !status.LoggedIn {
		return nil, []string{"dashboard scope unavailable: not logged in"}
	}
	if !status.Kubeconfig.Present {
		return nil, []string{"dashboard scope unavailable: active kubeconfig is missing"}
	}

	authData, err := auth.LoadAuthDataFromDir(configDir)
	if err != nil {
		return nil, []string{fmt.Sprintf("dashboard scope unavailable: active auth data could not be loaded: %v", err)}
	}
	kubeconfigPath := filepath.Join(configDir, "kubeconfig")
	client, err := k8s.NewClient(kubeconfigPath, authData)
	if err != nil {
		return nil, []string{fmt.Sprintf("dashboard scope unavailable: active kubeconfig could not be used: %v", err)}
	}
	namespace := client.Namespace()
	if namespace == "" {
		namespace = "default"
	}
	return &dashboardScope{
		region:    authData.Region,
		namespace: namespace,
		client:    client,
	}, nil
}

func dashboardScopedSessions(configDir string, scope *dashboardScope) ([]session.TunnelSession, []string, error) {
	if configDir == "" {
		return nil, []string{"dashboard skipped session loading because active config directory is unavailable"}, nil
	}
	if info, err := os.Lstat(configDir); os.IsNotExist(err) {
		return []session.TunnelSession{}, nil, nil
	} else if err != nil {
		return nil, nil, err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, nil, fmt.Errorf("active config directory %s is not a directory", configDir)
	}
	sessionsDir := filepath.Join(configDir, "sessions")
	if info, err := os.Lstat(sessionsDir); os.IsNotExist(err) {
		return []session.TunnelSession{}, nil, nil
	} else if err != nil {
		return nil, nil, err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, nil, fmt.Errorf("active sessions directory %s is not a directory", sessionsDir)
	}
	sessions, err := session.ListFromConfigDir(configDir)
	if err != nil {
		return nil, nil, fmt.Errorf("load tunnel sessions: %w", err)
	}
	if scope == nil {
		if len(sessions) == 0 {
			return []session.TunnelSession{}, nil, nil
		}
		return []session.TunnelSession{}, []string{fmt.Sprintf("dashboard skipped %d session(s) because active login scope is unavailable", len(sessions))}, nil
	}

	filtered := make([]session.TunnelSession, 0, len(sessions))
	skipped := 0
	for _, sess := range sessions {
		if sess.Region == scope.region && sess.Namespace == scope.namespace {
			filtered = append(filtered, sess)
			continue
		}
		skipped++
	}
	if skipped == 0 {
		return filtered, nil, nil
	}
	return filtered, []string{fmt.Sprintf("dashboard skipped %d session(s) outside active region %s namespace %s", skipped, scope.region, scope.namespace)}, nil
}

func dashboardRemoteDiagnostics(scope *dashboardScope) remoteDiagnosticsCollector {
	return func(ctx context.Context, sess session.TunnelSession) (*k8s.TunnelDiagnostics, error) {
		if scope == nil || scope.client == nil {
			return nil, fmt.Errorf("dashboard remote diagnostics require the active login kubeconfig")
		}
		if sess.Region != scope.region || sess.Namespace != scope.namespace {
			return nil, fmt.Errorf("session %s is outside the active dashboard scope", sess.TunnelID)
		}
		return collectRemoteDiagnosticsWithClient(ctx, sess, scope.client)
	}
}

func validateDashboardListen(addr string, allowRemote bool) (bool, error) {
	loopback := isDashboardLoopbackAddr(addr)
	if !loopback && !allowRemote {
		return false, fmt.Errorf("refusing to expose dashboard on non-loopback address %q; use --allow-remote only on trusted networks", addr)
	}
	return loopback, nil
}

func newDashboardToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate dashboard token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func isDashboardLoopbackAddr(addr string) bool {
	if addr == "localhost" {
		return true
	}
	ip := net.ParseIP(addr)
	return ip != nil && ip.IsLoopback()
}

var dashboardHTML = template.Must(template.New("dashboard").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Sealtun Control Center</title>
  <link rel="icon" type="image/svg+xml" href="/favicon.svg">
  <meta name="theme-color" content="#007A62">
  <style>
    :root {
      color-scheme: light;
      --bg: #f7f6f3;
      --surface: #ffffff;
      --surface-soft: #fbfaf8;
      --line: #e6e2dc;
      --line-strong: #d8d3cb;
      --ink: #171717;
      --text: #33312e;
      --muted: #746f68;
      --faint: #a19a90;
      --accent: #007a62;
      --accent-soft: #edf7f3;
      --warn: #d79a00;
      --warn-soft: #fff8e6;
      --bad: #db3340;
      --bad-soft: #fff0f1;
      --stale: #74716a;
      --shadow: 0 1px 2px rgba(22, 22, 20, 0.04), 0 18px 48px rgba(22, 22, 20, 0.05);
      --mono: "JetBrains Mono", "SFMono-Regular", Consolas, Menlo, monospace;
      --sans: "Geist", "Satoshi", "Avenir Next", "Helvetica Neue", Arial, sans-serif;
    }
    * { box-sizing: border-box; }
    html, body { margin: 0; min-height: 100%; }
    body {
      min-width: 1280px;
      min-height: 100dvh;
      background: var(--bg);
      color: var(--text);
      font-family: var(--sans);
      letter-spacing: -0.01em;
    }
    button {
      border: 0;
      background: none;
      font: inherit;
      color: inherit;
      cursor: pointer;
    }
    button:focus-visible, a:focus-visible {
      outline: 2px solid rgba(0, 122, 98, 0.35);
      outline-offset: 2px;
    }
    a { color: inherit; text-decoration: none; }
    .shell {
      min-height: 100dvh;
      display: grid;
      grid-template-rows: 64px minmax(0, 1fr);
    }
    .topbar {
      display: grid;
      grid-template-columns: auto 1fr auto;
      align-items: center;
      gap: 22px;
      height: 64px;
      padding: 0 22px;
      background: #ffffff;
      border-bottom: 1px solid var(--line);
    }
    .brand {
      display: flex;
      align-items: center;
      gap: 12px;
      padding-right: 20px;
      border-right: 1px solid var(--line);
    }
	.logo {
		width: 34px;
		height: 34px;
		display: grid;
		place-items: center;
	}
	.logo svg {
		width: 34px;
		height: 34px;
		display: block;
		filter: drop-shadow(0 1px 1px rgba(22,22,20,.10));
	}
	.logo img {
		width: 34px;
		height: 34px;
		display: block;
		filter: drop-shadow(0 1px 1px rgba(22,22,20,.10));
	}
    .brand-title {
      font-weight: 700;
      color: var(--ink);
      font-size: 16px;
      white-space: nowrap;
    }
    .context-bar {
      display: flex;
      align-items: center;
      gap: 34px;
      min-width: 0;
      color: var(--muted);
      font-size: 13px;
    }
    .context-wrap {
      position: relative;
      display: inline-flex;
    }
    .context-item {
      display: inline-flex;
      align-items: center;
      gap: 7px;
      white-space: nowrap;
      min-height: 34px;
      border-radius: 7px;
      padding: 0 8px;
      margin: 0 -8px;
      transition: background .15s;
    }
    .context-item:hover,
    .context-item[data-open="true"] { background: #f2f0ed; }
    .context-item strong {
      color: var(--ink);
      font-weight: 650;
    }
    .chevron {
      width: 8px;
      height: 8px;
      border-right: 1px solid var(--faint);
      border-bottom: 1px solid var(--faint);
      transform: rotate(45deg) translateY(-2px);
    }
	.context-menu {
		position: absolute;
		top: 42px;
		left: -8px;
		width: 430px;
      padding: 8px;
      z-index: 20;
      border: 1px solid var(--line-strong);
      border-radius: 10px;
      background: #ffffff;
      box-shadow: 0 18px 54px rgba(22, 22, 20, .14);
    }
    .context-menu[hidden] { display: none; }
	.context-menu.profile-menu { width: 430px; }
	.context-menu.namespace-menu { width: 430px; }
    .context-menu-title {
      padding: 7px 8px 9px;
      color: var(--muted);
      font-size: 12px;
      font-weight: 700;
      letter-spacing: .09em;
      text-transform: uppercase;
    }
	.context-row {
		width: 100%;
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		gap: 12px;
		align-items: center;
		min-height: 58px;
		padding: 10px 12px;
		border-radius: 8px;
		text-align: left;
		color: var(--text);
	}
	.context-row > span {
		display: block;
		min-width: 0;
	}
    .context-row:hover { background: #f5f4f1; }
    .context-row[disabled] {
      cursor: default;
      opacity: .66;
    }
    .context-row[disabled]:hover { background: transparent; }
	.context-name {
		display: block;
		color: var(--ink);
		font-weight: 650;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.context-sub {
		display: block;
		margin-top: 5px;
		color: var(--muted);
		font-size: 12px;
		line-height: 1.35;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
    .context-note {
      margin-top: 8px;
      padding: 10px;
      border-radius: 8px;
      background: #fbfaf7;
      color: var(--muted);
      font-size: 12px;
      line-height: 1.45;
    }
    .context-badge {
		color: var(--accent);
		font-size: 12px;
		font-weight: 700;
		white-space: nowrap;
	}
    .live-badge {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      min-height: 26px;
      padding: 0 9px;
      border: 1px solid #b9ded1;
      border-radius: 999px;
      background: #edf7f3;
      color: var(--accent);
      font-size: 12px;
      font-weight: 700;
    }
    .live-badge.reconnecting,
    .live-badge.polling {
      border-color: #f0d69b;
      background: #fff7df;
      color: #8a6100;
    }
    .live-badge.disconnected {
      border-color: #efc2c8;
      background: var(--bad-soft);
      color: var(--bad);
    }
    .toast {
      position: fixed;
      top: 76px;
      right: 382px;
      z-index: 30;
      max-width: 360px;
      padding: 10px 12px;
      border: 1px solid #b9ded1;
      border-radius: 9px;
      background: #edf7f3;
      color: var(--accent);
      box-shadow: 0 10px 30px rgba(22, 22, 20, .10);
      font-size: 13px;
      font-weight: 650;
    }
    .toast.error {
      border-color: #f1c1c5;
      background: #fff0f1;
      color: var(--bad);
    }
    .toast[hidden] { display: none; }
    .top-actions {
      display: flex;
      align-items: center;
      gap: 18px;
      color: var(--muted);
      font-size: 13px;
    }
    .daemon {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      white-space: nowrap;
    }
    .top-divider {
      width: 1px;
      height: 22px;
      background: var(--line-strong);
    }
    .refresh-time {
      font-family: var(--mono);
      color: var(--text);
    }
    .btn {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      min-height: 34px;
      padding: 0 12px;
      border: 1px solid var(--line-strong);
      border-radius: 7px;
      background: #ffffff;
      box-shadow: 0 1px 2px rgba(22,22,20,.04);
      color: var(--ink);
      transition: background .15s, border-color .15s, transform .15s;
    }
    .btn:hover { background: var(--surface-soft); border-color: #c8c2b8; }
    .btn:active { transform: translateY(1px); }
    .btn:disabled { opacity: .62; cursor: wait; }
    .btn svg { width: 16px; height: 16px; }
    .btn.primary {
      border-color: #007a62;
      background: #007a62;
      color: #ffffff;
    }
    .btn.danger {
      border-color: #f1c1c5;
      color: var(--bad);
    }
    .remote-warning {
      display: none;
      margin-bottom: 14px;
      padding: 12px 14px;
      border: 1px solid #f3d993;
      border-radius: 8px;
      background: #fff8e6;
      color: #7a5600;
      font-size: 13px;
    }
    .remote-warning[data-visible="true"] { display: block; }
    .toolbar {
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 12px;
      margin-bottom: 14px;
    }
    .toolbar-actions {
      display: inline-flex;
      gap: 10px;
      align-items: center;
    }
    .body {
      display: grid;
      grid-template-columns: minmax(0, 1fr) 360px;
      min-height: 0;
    }
    .main {
      min-width: 0;
      padding: 28px 24px 40px;
      overflow: auto;
    }
    .inspect-panel {
      background: #ffffff;
      border-left: 1px solid var(--line);
      min-height: calc(100dvh - 64px);
    }
    .cards {
      display: grid;
      grid-template-columns: repeat(6, minmax(0, 1fr));
      gap: 14px;
      margin-bottom: 28px;
    }
    .metric-card {
      min-height: 86px;
      border: 1px solid var(--line-strong);
      border-radius: 8px;
      background: #ffffff;
      padding: 16px 18px;
      display: grid;
      grid-template-columns: 28px 1fr;
      gap: 14px;
      align-items: center;
      box-shadow: 0 1px 2px rgba(22,22,20,.03);
    }
    .metric-card.active, .metric-card.domain { background: var(--accent-soft); border-color: #b9ded1; }
    .metric-card.degraded { background: var(--warn-soft); border-color: #f3d993; }
    .metric-card.issue { background: var(--bad-soft); border-color: #f1c1c5; }
    .metric-icon {
      color: var(--muted);
      width: 22px;
      height: 22px;
      display: grid;
      place-items: center;
    }
    .metric-icon svg { width: 19px; height: 19px; }
    .metric-card.active .metric-icon, .metric-card.domain .metric-icon { color: var(--accent); }
    .metric-card.degraded .metric-icon { color: var(--warn); }
    .metric-card.issue .metric-icon { color: var(--bad); }
    .metric-label {
      color: var(--muted);
      font-size: 13px;
      margin-bottom: 5px;
    }
    .metric-value {
      color: var(--ink);
      font-family: var(--mono);
      font-size: 23px;
      line-height: 1;
      font-weight: 700;
    }
    .section {
      border: 1px solid var(--line);
      background: #ffffff;
      border-radius: 9px;
      box-shadow: var(--shadow);
      overflow: hidden;
    }
    .section-head {
      height: 54px;
      padding: 0 18px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      border-bottom: 1px solid var(--line);
    }
    .section-title {
      color: var(--ink);
      font-weight: 700;
      font-size: 15px;
    }
    .section-meta {
      color: var(--muted);
      font-family: var(--mono);
      font-size: 12px;
    }
    .table-wrap { overflow-x: auto; }
	table {
		width: 100%;
		min-width: 840px;
		border-collapse: collapse;
	}
    th, td {
      border-bottom: 1px solid var(--line);
      text-align: left;
      padding: 12px 10px;
      font-size: 13px;
      vertical-align: middle;
    }
    th {
      color: var(--ink);
      font-weight: 600;
      background: #fbfaf7;
    }
    td { color: var(--text); }
    tr[data-selected="true"] td {
      background: #f4faf7;
    }
    .status {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      font-weight: 650;
      white-space: nowrap;
    }
    .dot {
      width: 9px;
      height: 9px;
      border-radius: 2px;
      background: var(--accent);
      display: inline-block;
    }
    .dot.round { border-radius: 99px; }
    .status.active { color: var(--accent); }
    .status.degraded { color: var(--warn); }
    .status.connecting { color: var(--accent); }
    .status.error { color: var(--bad); }
    .status.stale { color: var(--stale); }
    .status.degraded .dot { background: var(--warn); }
    .status.error .dot { background: var(--bad); }
    .status.stale .dot { background: var(--stale); }
    .tag {
      display: inline-flex;
      min-height: 24px;
      align-items: center;
      padding: 0 8px;
      border-radius: 6px;
      background: #f0efed;
      font-size: 12px;
      font-family: var(--mono);
      color: var(--ink);
    }
    .mode {
      display: inline-flex;
      min-height: 24px;
      align-items: center;
      padding: 0 10px;
      border-radius: 7px;
      border: 1px solid var(--line);
      background: #f5f4f1;
      font-size: 12px;
      color: var(--ink);
    }
    .mono {
      font-family: var(--mono);
      letter-spacing: -0.02em;
    }
    .muted { color: var(--muted); }
    .link {
      color: var(--accent);
      display: inline-flex;
      align-items: center;
      gap: 4px;
    }
    .link svg { width: 13px; height: 13px; }
    .actions {
      display: inline-flex;
      align-items: center;
      gap: 12px;
      flex-wrap: wrap;
      white-space: nowrap;
    }
    .action-text {
      color: var(--ink);
      font-size: 12px;
      font-weight: 600;
    }
    .action-text.danger { color: var(--bad); }
    .icon-btn {
      width: 24px;
      height: 24px;
      display: inline-grid;
      place-items: center;
      border-radius: 5px;
      color: var(--ink);
    }
    .icon-btn:hover { background: #f0efed; }
    .icon-btn svg { width: 17px; height: 17px; }
    .tabs {
      display: inline-flex;
      margin: 24px 0;
      border-radius: 8px;
      background: #ecebea;
      padding: 2px;
    }
    .tab {
      min-width: 58px;
      height: 34px;
      border-radius: 7px;
      padding: 0 12px;
      color: var(--ink);
      font-size: 13px;
    }
    .tab.active {
      background: #ffffff;
      box-shadow: 0 1px 3px rgba(22,22,20,.14);
    }
    .bottom-panel {
      border: 1px solid var(--line);
      background: #ffffff;
      border-radius: 9px;
      overflow: hidden;
      box-shadow: var(--shadow);
    }
    .bottom-head {
      height: 44px;
      padding: 0 18px;
      border-bottom: 1px solid var(--line);
      display: flex;
      align-items: center;
      justify-content: space-between;
    }
    .bottom-title {
      display: inline-flex;
      align-items: center;
      gap: 9px;
      color: var(--ink);
      font-weight: 700;
      font-size: 13px;
    }
    .follow {
      display: inline-flex;
      align-items: center;
      gap: 9px;
      color: var(--muted);
      font-size: 13px;
    }
    .switch {
      width: 28px;
      height: 16px;
      border-radius: 99px;
      background: var(--accent);
      position: relative;
    }
    .switch::after {
      content: "";
      position: absolute;
      top: 2px;
      right: 2px;
      width: 12px;
      height: 12px;
      border-radius: 99px;
      background: #ffffff;
    }
    .log-box {
      height: 184px;
      overflow: auto;
      padding: 14px 16px;
      background: #fbfaf7;
      font-family: var(--mono);
      font-size: 13px;
      line-height: 1.8;
      color: var(--accent);
    }
    .log-line {
      display: grid;
      grid-template-columns: 82px 42px minmax(0, 1fr);
      gap: 10px;
      align-items: baseline;
    }
    .log-time { color: var(--faint); }
    .level {
      display: inline-flex;
      justify-content: center;
      border-radius: 4px;
      padding: 0 5px;
      background: #d7f0e7;
      color: var(--accent);
      font-size: 11px;
    }
    .level.warn {
      background: #fff0bf;
      color: var(--warn);
    }
    .level.debug {
      background: #eeeeeb;
      color: var(--muted);
    }
    .panel-grid {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 12px;
      padding: 16px;
    }
    .small-card {
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fbfaf7;
      padding: 14px;
    }
    .small-card .label {
      color: var(--muted);
      font-size: 12px;
      margin-bottom: 8px;
    }
    .small-card .value {
      color: var(--ink);
      font-family: var(--mono);
      font-size: 18px;
      font-weight: 700;
    }
    .input.compact {
      max-width: 120px;
      height: 32px;
      padding: 6px 8px;
    }
    .checkline {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      color: var(--ink);
      font-size: 12px;
      font-weight: 650;
    }
    .checkline input {
      width: 14px;
      height: 14px;
    }
    .yaml {
      padding: 16px;
      background: #171717;
      color: #f3f0e8;
      font-family: var(--mono);
      font-size: 13px;
      line-height: 1.65;
      white-space: pre-wrap;
      min-height: 184px;
    }
    .inspect-head {
      height: 60px;
      padding: 0 18px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      border-bottom: 1px solid var(--line);
    }
    .inspect-title {
      display: flex;
      align-items: center;
      gap: 10px;
      color: var(--ink);
      font-weight: 700;
    }
    .close {
      color: var(--ink);
      font-size: 22px;
      line-height: 1;
    }
    .inspect-body {
      padding: 18px;
    }
    .inspect-summary {
      padding-bottom: 18px;
      border-bottom: 1px solid var(--line);
    }
    .inspect-summary .status {
      font-size: 15px;
      margin-bottom: 8px;
    }
	.inspect-url {
		color: var(--accent);
		font-size: 13px;
		overflow-wrap: anywhere;
	}
	.inspect-url svg {
		width: 13px;
		height: 13px;
		margin-left: 4px;
		vertical-align: -2px;
	}
    .inspect-group {
      padding: 18px 0;
      border-bottom: 1px solid var(--line);
    }
    .group-title {
      color: var(--muted);
      font-weight: 700;
      letter-spacing: .11em;
      text-transform: uppercase;
      font-size: 12px;
      margin-bottom: 13px;
    }
    .kv {
      display: grid;
      gap: 12px;
    }
    .kv-row {
      display: grid;
      grid-template-columns: 118px minmax(0, 1fr) 22px;
      gap: 10px;
      align-items: center;
      min-height: 26px;
      color: var(--muted);
      font-size: 13px;
    }
    .kv-row .value {
      justify-self: end;
      min-width: 0;
      max-width: 190px;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      color: var(--ink);
    }
    .kv-row .value.mono {
      background: #efeeeb;
      border-radius: 5px;
      padding: 4px 7px;
    }
    .copy {
      color: var(--muted);
      display: grid;
      place-items: center;
    }
    .copy svg { width: 16px; height: 16px; }
    .yes { color: var(--accent); font-weight: 650; }
    .no { color: var(--bad); font-weight: 650; }
    .empty {
      height: 278px;
      display: grid;
      place-items: center;
      text-align: center;
      padding: 24px;
      color: var(--muted);
      background: #fbfaf7;
    }
    .empty strong {
      display: block;
      color: var(--ink);
      margin-bottom: 8px;
      font-size: 15px;
    }
    .modal-backdrop {
      position: fixed;
      inset: 0;
      z-index: 50;
      background: rgba(23,23,23,.32);
      display: grid;
      place-items: center;
      padding: 28px;
    }
    .modal-backdrop[hidden] { display: none; }
    .modal {
      width: min(760px, 100%);
      max-height: calc(100dvh - 56px);
      overflow: auto;
      border-radius: 10px;
      background: #ffffff;
      box-shadow: 0 30px 80px rgba(0,0,0,.22);
      border: 1px solid var(--line-strong);
    }
    .modal-head {
      height: 58px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 0 18px;
      border-bottom: 1px solid var(--line);
    }
    .modal-title {
      color: var(--ink);
      font-weight: 700;
    }
    .modal-body {
      padding: 18px;
      display: grid;
      gap: 14px;
    }
    .form-grid {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 12px;
    }
    .field {
      display: grid;
      gap: 6px;
      color: var(--muted);
      font-size: 12px;
      font-weight: 650;
    }
    .field.full { grid-column: 1 / -1; }
    .input, .select, .textarea {
      width: 100%;
      border: 1px solid var(--line-strong);
      border-radius: 7px;
      background: #ffffff;
      color: var(--ink);
      font: 13px var(--sans);
      padding: 9px 10px;
      outline: none;
    }
    .textarea {
      min-height: 280px;
      resize: vertical;
      font-family: var(--mono);
      line-height: 1.55;
    }
    .template-pills {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
    }
    .pill {
      min-height: 30px;
      padding: 0 10px;
      border: 1px solid var(--line-strong);
      border-radius: 7px;
      background: #fbfaf7;
      font-size: 12px;
      color: var(--ink);
    }
    .modal-actions {
      display: flex;
      justify-content: flex-end;
      gap: 10px;
      padding: 14px 18px 18px;
      border-top: 1px solid var(--line);
    }
    .result-box {
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fbfaf7;
      padding: 12px;
      font-family: var(--mono);
      font-size: 12px;
      line-height: 1.55;
      white-space: pre-wrap;
      max-height: 260px;
      overflow: auto;
    }
    .command-preview {
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #161614;
      color: #f7f4ec;
      padding: 12px;
      font-family: var(--mono);
      font-size: 12px;
      line-height: 1.55;
      overflow-x: auto;
    }
    .discover-list,
    .resource-list {
      display: grid;
      gap: 8px;
    }
    .discover-row,
    .resource-row {
      display: grid;
      grid-template-columns: 86px minmax(0, 1fr) auto;
      gap: 12px;
      align-items: center;
      min-height: 48px;
      padding: 10px 12px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: #fbfaf7;
      text-align: left;
    }
    .resource-row {
      grid-template-columns: 140px minmax(0, 1fr) 150px;
      align-items: start;
    }
    .resource-hints {
      margin-top: 6px;
      color: var(--muted);
      font-size: 12px;
      line-height: 1.45;
    }
  </style>
</head>
<body>
  <div class="shell">
    <header class="topbar">
      <div class="brand">
        <div class="logo" aria-hidden="true">
          <img src="/logo.svg" alt="">
        </div>
        <div class="brand-title">Sealtun Control Center</div>
      </div>
      <div class="context-bar">
        <div class="context-wrap">
          <button class="context-item" data-menu="region" type="button">Region: <strong id="region">-</strong><span class="chevron"></span></button>
          <div class="context-menu" id="menu-region" hidden></div>
        </div>
        <div class="context-wrap">
          <button class="context-item" data-menu="namespace" type="button">Namespace: <strong id="namespace">-</strong><span class="chevron"></span></button>
          <div class="context-menu namespace-menu" id="menu-namespace" hidden></div>
        </div>
        <div class="context-wrap">
          <button class="context-item" data-menu="profile" type="button">Profile: <strong id="profile">default</strong><span class="chevron"></span></button>
          <div class="context-menu profile-menu" id="menu-profile" hidden></div>
        </div>
      </div>
      <div class="top-actions">
        <span class="daemon" id="daemon-state"><span class="dot round"></span>Daemon running</span>
        <span class="top-divider"></span>
        <span class="live-badge polling" id="live-state"><span class="dot round"></span>Polling</span>
        <span class="top-divider"></span>
        <span>Last updated: <span class="refresh-time" id="updated">--:--:--</span></span>
        <button class="btn" id="refresh-btn" type="button">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M20 12a8 8 0 0 1-13.66 5.66"/><path d="M4 12A8 8 0 0 1 17.66 6.34"/><path d="M20 5v5h-5"/><path d="M4 19v-5h5"/></svg>
          Refresh
        </button>
      </div>
    </header>

    <div class="body">
      <main class="main">
        <div class="remote-warning" id="remote-warning" data-visible="{{ .Remote }}">Remote dashboard mode is enabled. Mutating operations are allowed here, but every write action requires confirmation.</div>
        <div class="toolbar">
          <div class="section-title">Workspace</div>
          <div class="toolbar-actions">
            <button class="btn primary" id="new-tunnel-btn" type="button">New Tunnel</button>
            <button class="btn" id="apply-yaml-btn" type="button">Apply YAML</button>
          </div>
        </div>
        <section class="cards" id="cards"></section>

        <section class="section">
          <div class="section-head">
            <div class="section-title">Tunnels</div>
            <div class="section-meta" id="tunnel-meta">0 records</div>
          </div>
          <div class="table-wrap" id="tunnel-table"></div>
        </section>

        <nav class="tabs" aria-label="Tunnel tools">
          <button class="tab active" data-tab="logs" type="button">Logs</button>
          <button class="tab" data-tab="metrics" type="button">Metrics</button>
          <button class="tab" data-tab="events" type="button">Events</button>
          <button class="tab" data-tab="resources" type="button">Resources</button>
          <button class="tab" data-tab="audit" type="button">Audit</button>
          <button class="tab" data-tab="domain" type="button">Domain</button>
          <button class="tab" data-tab="config" type="button">Config</button>
        </nav>

        <section class="bottom-panel">
          <div class="bottom-head">
            <div class="bottom-title"><span id="tab-title">Logs</span><span class="tag" id="tab-tunnel">-</span></div>
            <div class="follow" id="tab-action"><span class="dot round"></span>Follow <span class="switch"></span></div>
          </div>
          <div id="tab-content"></div>
        </section>
      </main>

      <aside class="inspect-panel" id="inspect-panel"></aside>
    </div>
  </div>
  <div class="toast" id="toast" hidden></div>
  <div class="modal-backdrop" id="modal-backdrop" hidden></div>

  <script>
    const embeddedDashboardToken = "{{ .Token }}";
    const remoteDashboardMode = "{{ .Remote }}" === "true";
    const tokenFromHash = new URLSearchParams(window.location.hash.replace(/^#/, "")).get("token") || "";
    if (tokenFromHash) {
      sessionStorage.setItem("sealtunDashboardToken", tokenFromHash);
      window.history.replaceState(null, "", window.location.pathname + window.location.search);
    }
	    const dashboardToken = embeddedDashboardToken || tokenFromHash || sessionStorage.getItem("sealtunDashboardToken") || "";
	    let snapshot = null;
	    let contextSnapshot = null;
    let selectedTunnel = "";
    let activeTab = "logs";
    let openMenu = "";
    let refreshInFlight = null;
    let tabDataCache = {};
    let pollingTimer = null;
    let watchAbort = null;
    let watchGeneration = 0;
    let liveMode = "polling";

	    const esc = (v) => String(v ?? "").replace(/[&<>"']/g, ch => ({ "&":"&amp;", "<":"&lt;", ">":"&gt;", '"':"&quot;", "'":"&#39;" }[ch]));
	    const dnsHostPattern = /^(?=.{1,253}$)(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/i;
	    const safeHost = (v) => {
	      const host = String(v || "").trim().replace(/\.$/, "").toLowerCase();
	      return dnsHostPattern.test(host) ? host : "";
	    };
	    const publicHostFor = (t) => safeHost(t?.host) || safeHost(t?.sealosHost);
	    const publicEndpointFor = (t) => {
	      const host = publicHostFor(t);
	      if (!host) return "";
	      if (t?.protocol === "ssh" && t?.publicPort) return "ssh <user>@" + host + " -p " + t.publicPort;
	      if (t?.protocol === "tcp" && t?.publicPort) return host + ":" + t.publicPort;
	      return "https://" + host;
	    };
	    const hostLink = (t, className) => {
	      const host = publicHostFor(t);
	      const safe = safeHost(host);
	      if (!safe) return '<span class="' + esc(className || "link") + ' muted">-</span>';
	      if (t?.protocol === "ssh" || t?.protocol === "tcp") return '<span class="' + esc(className || "link") + ' mono">' + esc(publicEndpointFor(t) || safe) + '</span>';
	      return '<a class="' + esc(className || "link") + '" href="https://' + esc(safe) + '" target="_blank" rel="noreferrer">https://' + esc(safe) + externalIcon + '</a>';
	    };
	    const yes = (v) => v ? '<span class="yes">Yes</span>' : '<span class="no">No</span>';
    const statusClass = (status) => {
      if (status === "active" || status === "running") return "active";
      if (status === "degraded") return "degraded";
      if (status === "connecting") return "connecting";
      if (status === "error") return "error";
      return "stale";
    };
    const tunnelByID = (id) => (snapshot?.tunnels || []).find(t => t.tunnelId === id);
    const selected = () => tunnelByID(selectedTunnel) || (snapshot?.tunnels || [])[0] || null;
    const copyIcon = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><rect x="8" y="8" width="11" height="11" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v1"/></svg>';
    const externalIcon = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M14 5h5v5"/><path d="m10 14 9-9"/><path d="M19 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h4"/></svg>';
    const terminalIcon = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="m4 7 5 5-5 5"/><path d="M12 19h8"/></svg>';
    const metricsIcon = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M5 19V9"/><path d="M12 19V5"/><path d="M19 19v-7"/><path d="M3 19h18"/></svg>';
    const globeIcon = '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3c2.4 2.5 3.6 5.5 3.6 9S14.4 18.5 12 21c-2.4-2.5-3.6-5.5-3.6-9S9.6 5.5 12 3Z"/></svg>';
    const protocolDefaults = {
      https: { name: "web", port: 3000, protocol: "https" },
      ssh: { name: "ssh", port: 22, protocol: "ssh" },
      tcp: { name: "tcp", port: 9000, protocol: "tcp" },
      mysql: { name: "mysql", port: 3306, protocol: "tcp" },
      postgres: { name: "postgres", port: 5432, protocol: "tcp" },
      redis: { name: "redis", port: 6379, protocol: "tcp" },
      mqtt: { name: "mqtt", port: 1883, protocol: "tcp" }
    };

    function render(data) {
      snapshot = data;
      const tunnels = data.tunnels || [];
      if (!selectedTunnel && tunnels.length) selectedTunnel = tunnels[0].tunnelId;
      if (selectedTunnel && !tunnelByID(selectedTunnel) && tunnels.length) selectedTunnel = tunnels[0].tunnelId;
      renderHeader(data);
      renderCards(data);
      renderTable(data);
      renderInspect();
      renderTab();
      wire();
    }

    function renderHeader(data) {
      const status = data.status || {};
      const kube = status.kubeconfig || {};
      const doctor = data.doctor || {};
      const loggedIn = Boolean(status.loggedIn);
      document.getElementById("region").textContent = loggedIn ? (status.region || "-") : "-";
      document.getElementById("namespace").textContent = loggedIn && kube.present ? (kube.namespace || "-") : "-";
      document.getElementById("profile").textContent = loggedIn ? (status.activeProfile || "active") : "-";
      document.querySelectorAll("[data-menu]").forEach(btn => {
        btn.dataset.empty = String(!loggedIn);
      });
      document.getElementById("updated").textContent = (data.generatedAt || "").split("T")[1]?.slice(0, 8) || "--:--:--";
      document.getElementById("daemon-state").innerHTML = '<span class="dot round"></span>' + (doctor.daemonRunning ? "Daemon running" : "Daemon offline");
      document.getElementById("daemon-state").className = "daemon " + (doctor.daemonRunning ? "" : "status error");
    }

    async function loadContext() {
      if (contextSnapshot) return contextSnapshot;
      const res = await fetch("/api/context", { cache: "no-store", headers: { "X-Sealtun-Dashboard-Token": dashboardToken } });
      if (!res.ok) throw new Error("Context API returned " + res.status);
      contextSnapshot = await res.json();
      return contextSnapshot;
    }

    async function toggleContextMenu(name) {
      if (openMenu === name) {
        closeContextMenus();
        return;
      }
      openMenu = name;
      document.querySelectorAll(".context-menu").forEach(menu => menu.hidden = true);
      document.querySelectorAll("[data-menu]").forEach(btn => btn.dataset.open = String(btn.dataset.menu === name));
      const menu = document.getElementById("menu-" + name);
      menu.hidden = false;
      menu.innerHTML = '<div class="context-note">Loading...</div>';
      try {
        const ctx = await loadContext();
        renderContextMenu(name, ctx);
      } catch (err) {
        menu.innerHTML = '<div class="context-note">' + esc(err.message || err) + '</div>';
      }
    }

    function closeContextMenus() {
      openMenu = "";
      document.querySelectorAll(".context-menu").forEach(menu => menu.hidden = true);
      document.querySelectorAll("[data-menu]").forEach(btn => btn.dataset.open = "false");
    }

    function renderContextMenu(name, ctx) {
      if (name === "region") renderRegionMenu(ctx);
      if (name === "namespace") renderNamespaceMenu();
      if (name === "profile") renderProfileMenu(ctx);
      wire();
    }

    function renderRegionMenu(ctx) {
      const menu = document.getElementById("menu-region");
      if (!snapshot?.status?.loggedIn) {
        menu.innerHTML =
          '<div class="context-menu-title">No active region</div>' +
          '<div class="context-note">Local login has been cleared. Run <span class="mono">sealtun login</span> or <span class="mono">sealtun region use NAME</span> to select a region.</div>';
        return;
      }
      const regions = ctx.regions || [];
      menu.innerHTML =
        '<div class="context-menu-title">Active region</div>' +
        (regions.length ? regions.map(region => {
          const command = "sealtun region use " + (region.name || region.url || "");
          return '<button class="context-row" data-copy="' + esc(command) + '" type="button">' +
            '<span><span class="context-name">' + esc(region.name || region.url) + '</span><span class="context-sub">' + esc(region.url || "-") + ' · ' + esc(region.sealosDomain || "domain unknown") + '</span></span>' +
            '<span class="context-badge">' + (region.current ? "Current" : "Copy cmd") + '</span>' +
          '</button>';
        }).join("") : '<div class="context-note">No active region metadata available.</div>') +
        '<div class="context-note">Dashboard only shows the currently logged-in region. Use the CLI to switch login scope.</div>';
    }

    function renderNamespaceMenu() {
      const menu = document.getElementById("menu-namespace");
      const status = snapshot?.status || {};
      const kube = status.kubeconfig || {};
      if (!status.loggedIn || !kube.present) {
        menu.innerHTML =
          '<div class="context-menu-title">No active kubeconfig</div>' +
          '<div class="context-note">Local login has been cleared, so dashboard is not reading any Kubernetes context.</div>';
        return;
      }
      menu.innerHTML =
        '<div class="context-menu-title">Kubernetes context</div>' +
        readonlyContextRow("Namespace", kube.namespace || "-") +
        readonlyContextRow("Context", kube.currentContext || "-") +
        readonlyContextRow("Cluster", kube.cluster || "-") +
        readonlyContextRow("Kubeconfig", kube.path || "-") +
        '<div class="context-note">Namespace follows the active kubeconfig. Switch profile or re-login to use a different workspace/namespace.</div>';
    }

    function renderProfileMenu(ctx) {
      const menu = document.getElementById("menu-profile");
      const status = snapshot?.status || {};
      if (!status.loggedIn) {
        menu.innerHTML =
          '<div class="context-menu-title">No active profile</div>' +
          '<div class="context-note">Local login has been cleared. Dashboard does not read saved profiles or alternate kubeconfigs.</div>';
        return;
      }
      const activeProfile = status.activeProfile || "active";
      menu.innerHTML =
        '<div class="context-menu-title">Active login scope</div>' +
        readonlyContextRow("Profile", activeProfile) +
        readonlyContextRow("Region", status.region || "-") +
        readonlyContextRow("Ingress domain", status.sealosDomain || "-") +
        '<div class="context-note">Dashboard does not read saved profiles or alternate kubeconfigs. Use <span class="mono">sealtun profile use NAME</span> in the CLI, then refresh.</div>';
    }

    function readonlyContextRow(label, value) {
      return '<div class="context-row"><span><span class="context-name">' + esc(label) + '</span><span class="context-sub">' + esc(value) + '</span></span><button class="copy" data-copy="' + esc(value) + '" type="button">' + copyIcon + '</button></div>';
    }

    function showToast(message, isError = false) {
      const el = document.getElementById("toast");
      el.textContent = message;
      el.className = "toast" + (isError ? " error" : "");
      el.hidden = false;
      clearTimeout(showToast.timer);
      showToast.timer = setTimeout(() => { el.hidden = true; }, 2600);
    }

    function renderCards(data) {
      const d = data.doctor || {};
      const domains = data.domains || {};
      const cards = [
        ["Total Sessions", d.totalSessions || 0, "", pulseIcon()],
        ["Active Tunnels", d.activeSessions || 0, "active", swapIcon()],
        ["Degraded Tunnels", d.degradedSessions || 0, "degraded", warnIcon()],
        ["Remote Issues", d.remoteIssues || 0, "issue", serverIcon()],
        ["Domains Ready", domains.ready || 0, "domain", globeIcon],
        ["Local Ports", d.reachableActivePorts || 0, "", plugIcon()]
      ];
      document.getElementById("cards").innerHTML = cards.map(c =>
        '<div class="metric-card ' + c[2] + '"><div class="metric-icon">' + c[3] + '</div><div><div class="metric-label">' + esc(c[0]) + '</div><div class="metric-value">' + esc(c[1]) + '</div></div></div>'
      ).join("");
    }

    function renderTable(data) {
      const tunnels = data.tunnels || [];
      document.getElementById("tunnel-meta").textContent = tunnels.length + " records";
      if (!tunnels.length) {
        document.getElementById("tunnel-table").innerHTML =
          '<div class="empty"><div><strong>No local tunnel sessions</strong><div>Run <span class="mono">sealtun expose 3000</span> or <span class="mono">sealtun apply -f sealtun.yaml</span> to create one.</div></div></div>';
        return;
      }
	      document.getElementById("tunnel-table").innerHTML =
	        '<table><thead><tr><th>Status</th><th>Tunnel ID</th><th>Public Endpoint</th><th>Local Target</th><th>Mode</th><th>Namespace</th><th>Created At</th><th>Actions</th></tr></thead><tbody>' +
	        tunnels.map(t => {
	          const cls = statusClass(t.status);
	          const host = publicHostFor(t);
	          return '<tr data-selected="' + (t.tunnelId === selectedTunnel) + '">' +
	            '<td><button class="status ' + cls + '" data-select="' + esc(t.tunnelId) + '"><span class="dot"></span>' + esc(title(t.status)) + '</button></td>' +
	            '<td><span class="tag">' + esc(t.tunnelId) + '</span></td>' +
	            '<td>' + hostLink(t, "link") + '</td>' +
	            '<td class="mono muted">localhost:' + esc(t.localPort || "-") + '</td>' +
            '<td><span class="mode">' + esc(t.mode || "-") + '</span></td>' +
            '<td class="muted">' + esc(t.namespace || "-") + '</td>' +
            '<td class="mono muted">' + esc(shortDate(t.createdAt)) + '</td>' +
            '<td><div class="actions">' +
              '<button class="action-text" data-select="' + esc(t.tunnelId) + '">Inspect</button>' +
              tunnelActionButtons(t) +
              '<button class="icon-btn" data-copy="sealtun logs ' + esc(t.tunnelId) + ' --tail 200">' + terminalIcon + '</button>' +
              '<button class="icon-btn" data-copy="sealtun metrics ' + esc(t.tunnelId) + '">' + metricsIcon + '</button>' +
              '<button class="icon-btn" data-copy="sealtun domain doctor ' + esc(t.tunnelId) + '">' + globeIcon + '</button>' +
            '</div></td>' +
          '</tr>';
        }).join("") +
        '</tbody></table>';
    }

    function tunnelActionButtons(t) {
      const id = esc(t.tunnelId);
      const status = t.status || "";
      let buttons = "";
      if (status === "stopped") buttons += '<button class="action-text" data-action="start" data-tunnel="' + id + '">Start</button>';
      if (status !== "stopped" && status !== "stale") buttons += '<button class="action-text" data-action="stop" data-tunnel="' + id + '">Stop</button>';
      if (status === "stopped" || status === "stale" || status === "error") buttons += '<button class="action-text danger" data-action="cleanup" data-tunnel="' + id + '">Cleanup</button>';
      return buttons;
    }

    function renderInspect() {
      const t = selected();
      if (!t) {
        document.getElementById("inspect-panel").innerHTML =
          '<div class="inspect-head"><div class="inspect-title">Inspect</div><button class="close" type="button">x</button></div>' +
          '<div class="inspect-body"><div class="empty"><div><strong>No tunnel selected</strong><div>Create or select a tunnel to inspect its connection, local status, and cloud resources.</div></div></div></div>';
        return;
	      }
	      const cls = statusClass(t.status);
	      const host = publicHostFor(t);
	      const cname = safeHost(t.sealosHost) || host;
	      document.getElementById("inspect-panel").innerHTML =
	        '<div class="inspect-head"><div class="inspect-title">Inspect <span class="tag">' + esc(t.tunnelId) + '</span></div><button class="close" type="button">x</button></div>' +
	        '<div class="inspect-body">' +
	          '<div class="inspect-summary"><div class="status ' + cls + '"><span class="dot"></span>' + esc(title(t.status)) + '</div>' + hostLink(t, "inspect-url") + '</div>' +
            '<div class="inspect-group"><div class="group-title">Actions</div><div class="actions">' + tunnelActionButtons(t) + '</div></div>' +
	          group("Connection", [
	            [t.protocol === "ssh" ? "Public SSH" : (t.protocol === "tcp" ? "Public TCP" : "Public URL"), publicEndpointFor(t) || "-", true],
	            ["CNAME Target", cname || "-", true],
	            ["Local Target", "localhost:" + (t.localPort || "-"), true]
          ]) +
          group("Local Status", [
            ["Process Alive", isLive(t.status) ? "Yes" : "Unknown", false, isLive(t.status)],
            ["Port Reachable", t.status === "degraded" ? "No" : (isLive(t.status) ? "Yes" : "Unknown"), false, t.status !== "degraded" && isLive(t.status)]
          ]) +
          group("Remote Resources", [
            ["Deployment Ready", isLive(t.status) ? "Yes" : "Unknown", false, isLive(t.status)],
            ["Service Exists", isLive(t.status) ? "Yes" : "Unknown", false, isLive(t.status)],
            ["Ingress Hosts", host, true],
            ["Pod Ready", isLive(t.status) ? "Yes" : "Unknown", false, isLive(t.status)],
            ["Pod Restarts", "0", true]
          ]) +
        '</div>';
    }

    function renderTab() {
      const t = selected();
      const id = t?.tunnelId || "-";
      document.getElementById("tab-tunnel").textContent = id;
      document.getElementById("tab-title").textContent = title(activeTab);
      document.querySelectorAll(".tab").forEach(btn => btn.classList.toggle("active", btn.dataset.tab === activeTab));
      const target = document.getElementById("tab-content");
      if (!t) {
        target.innerHTML = '<div class="empty"><div><strong>No tunnel data</strong><div>Select a tunnel to view ' + esc(activeTab) + ' information.</div></div></div>';
        return;
      }
      if (activeTab === "logs") target.innerHTML = logsPanel(t);
      if (activeTab === "metrics") target.innerHTML = metricsPanel(t);
      if (activeTab === "events") target.innerHTML = eventsPanel(t);
      if (activeTab === "resources") target.innerHTML = resourcesPanel(t);
      if (activeTab === "audit") target.innerHTML = auditPanel(t);
      if (activeTab === "domain") target.innerHTML = domainPanel(t);
      if (activeTab === "config") target.innerHTML = configPanel(t);
      loadActiveTabData(t).catch(err => showToast(err.message || String(err), true));
    }

    function tabCache(t, key) {
      return tabDataCache[t?.tunnelId + ":" + key];
    }

    function setTabCache(t, key, data) {
      tabDataCache[t?.tunnelId + ":" + key] = data;
    }

    async function loadActiveTabData(t) {
      if (!t || activeTab === "domain" || activeTab === "config") return;
      if (tabCache(t, activeTab)) return;
      const id = encodeURIComponent(t.tunnelId);
      let data;
      if (activeTab === "logs") data = await apiFetch("/api/tunnels/" + id + "/logs?tail=200");
      if (activeTab === "metrics") data = await apiFetch("/api/tunnels/" + id + "/metrics");
      if (activeTab === "events") data = await apiFetch("/api/tunnels/" + id + "/events");
      if (activeTab === "resources") data = await apiFetch("/api/tunnels/" + id + "/resources");
      if (activeTab === "audit") data = {
        audit: await apiFetch("/api/tunnels/" + id + "/audit?since=10m&limit=200"),
        policy: await apiFetch("/api/tunnels/" + id + "/policy")
      };
      setTabCache(t, activeTab, data);
      if (selected()?.tunnelId === t.tunnelId) {
        renderTab();
        wire();
      }
    }

	    function logsPanel(t) {
	      return '<div class="log-box" id="logs-box">' + esc(tabCache(t, "logs")?.text || "Loading logs...") + '</div>';
    }

    function metricsPanel(t) {
      const data = tabCache(t, "metrics");
      if (!data) return '<div class="empty"><div><strong>Loading metrics</strong><div>Fetching remote and server counters.</div></div></div>';
      const remote = data.remote || {};
      const server = data.server || {};
      return '<div class="panel-grid">' +
        small("Status", data.status || title(t.status)) +
        small("Local Target", "localhost:" + (data.localPort || t.localPort || "-")) +
        small("Remote Ready", remote.deploymentReady || "-") +
        small("Pods", String(remote.readyPods || 0) + "/" + String(remote.podCount || 0)) +
        small("Requests", server.totalRequests ?? "-") +
        small("Active", server.activeRequests ?? server.activeTCPConnections ?? "-") +
      '</div>';
    }

    function eventsPanel(t) {
      const data = tabCache(t, "events");
      if (!data) return '<div class="empty"><div><strong>Loading events</strong><div>Fetching recent Kubernetes events.</div></div></div>';
      const events = data.events || [];
      if (!events.length) return '<div class="empty"><div><strong>No recent events</strong><div>Kubernetes did not report recent tunnel events.</div></div></div>';
      return '<div class="log-box">' + events.map(ev => line((ev.lastTimestamp || ev.firstTimestamp || "").slice(11, 19) || "--:--:--", ev.type || "Event", (ev.reason || "Event") + " " + (ev.object || "-") + ": " + (ev.message || ""))).join("") + '</div>';
    }

    function resourcesPanel(t) {
      const data = tabCache(t, "resources");
      if (!data) return '<div class="empty"><div><strong>Loading resources</strong><div>Fetching Kubernetes resources and resource occupancy hints.</div></div></div>';
      const resources = data.resources || [];
      if (!resources.length) return '<div class="empty"><div><strong>No resources</strong><div>No Kubernetes resources were reported for this tunnel.</div></div></div>';
      return '<div class="resource-list">' + resources.map(item => {
        const hints = (item.costHints || []).concat(item.warnings || []);
        return '<div class="resource-row">' +
          '<div><strong>' + esc(item.kind || "-") + '</strong><div class="muted mono">' + esc(item.namespace || "-") + '</div></div>' +
          '<div><div class="mono">' + esc(item.name || "-") + '</div><div class="resource-hints">' + esc(hints.join(" · ") || "No resource hints") + '</div></div>' +
          '<div><span class="tag">' + esc(item.status || "-") + '</span><div class="resource-hints">managed=' + esc(item.managed ? "yes" : "no") + (item.age ? " · age=" + esc(item.age) : "") + '</div></div>' +
        '</div>';
      }).join("") + '</div>';
    }

    function auditPanel(t) {
      const protocol = t.protocol || "https";
      if (protocol !== "https") {
        return '<div class="panel-grid">' +
          small("Access Policy", "HTTPS only") +
          small(protocol === "ssh" ? "Public SSH" : "Public TCP", publicEndpointFor(t) || "-") +
          small("Model", "Raw TCP NodePort") +
        '</div>';
      }
      const data = tabCache(t, "audit");
      if (!data) return '<div class="empty"><div><strong>Loading audit</strong><div>Fetching access policy and recent audit events.</div></div></div>';
      const policy = data.policy || {};
      const audit = data.audit || {};
      const events = audit.events || [];
      const rows = events.length ? events.map(ev =>
        '<div class="resource-row">' +
          '<div><strong>' + esc(ev.decision || "-") + '</strong><div class="muted mono">' + esc((ev.time || "").replace("T", " ").replace("Z", "")) + '</div></div>' +
          '<div><div class="mono">' + esc(ev.reason || "-") + '</div><div class="resource-hints">' + esc((ev.method || "-") + " " + (ev.path || "-")) + '</div></div>' +
          '<div><span class="tag">' + esc(ev.status || "-") + '</span><div class="resource-hints">' + esc(ev.clientIp || "-") + '</div></div>' +
        '</div>'
      ).join("") : '<div class="empty"><div><strong>No audit events</strong><div>No recent HTTPS access decisions were reported.</div></div></div>';
      return '<div class="panel-grid">' +
        small("Bearer Tokens", policy.bearerTokens || 0) +
        small("Rate Limit", policy.rateLimit || "off") +
        small("Audit", policy.auditEnabled ? "enabled" : "disabled") +
        '<div class="small-card"><div class="label">Policy</div><div class="actions"><input class="input compact" id="policy-rate-limit" placeholder="60/m" value="' + esc(policy.rateLimit || "") + '"><label class="checkline"><input id="policy-audit-enabled" type="checkbox" ' + (policy.auditEnabled ? "checked" : "") + '> Audit</label><button class="btn" data-policy-set="' + esc(t.tunnelId) + '" type="button">Set</button></div></div>' +
        '<div class="small-card"><div class="label">Share Rotate</div><div class="actions"><input class="input compact" id="share-rotate-name" placeholder="name"><input class="input compact" id="share-rotate-ttl" placeholder="1h"><button class="btn" data-share-rotate="' + esc(t.tunnelId) + '" type="button">Rotate</button></div></div>' +
        '<div class="small-card"><div class="label">Tunnel Credential</div><div class="actions"><button class="btn danger" data-rotate-secret="' + esc(t.tunnelId) + '" type="button">Rotate</button></div></div>' +
      '</div><div class="resource-list">' + rows + '</div>';
    }

	    function domainPanel(t) {
	      const items = snapshot?.domains?.items || [];
	      const item = items.find(x => x.tunnelId === t.tunnelId);
	      const host = safeHost(t.sealosHost) || publicHostFor(t) || "-";
      if (!item && !t.customDomain) {
        if (t.protocol === "ssh" || t.protocol === "tcp") {
          return '<div class="panel-grid">' +
            small("Custom Domain", "HTTPS tunnels only") +
            small(t.protocol === "ssh" ? "Public SSH" : "Public TCP", publicEndpointFor(t) || "-") +
            small("Command", "sealtun expose " + (t.localPort || (t.protocol === "ssh" ? "22" : "5432")) + " --protocol " + t.protocol) +
          '</div>';
        }
        return '<div class="panel-grid">' +
          small("Custom Domain", "Not configured") +
          small("CNAME Target", host) +
          '<div class="small-card"><div class="label">Actions</div><div class="actions"><button class="btn" data-domain-action="plan" data-tunnel="' + esc(t.tunnelId) + '" type="button">Plan</button><button class="btn" data-domain-action="add" data-tunnel="' + esc(t.tunnelId) + '" type="button">Add</button></div></div>' +
        '</div>';
      }
      return '<div class="panel-grid">' +
        small("Custom Domain", item?.customDomain || t.customDomain || "-") +
        small("DNS Ready", item?.dnsReady ? "Yes" : "Pending") +
        small("Certificate", item?.certificateReady ? "Ready" : "Pending") +
        '<div class="small-card"><div class="label">Actions</div><div class="actions"><button class="btn" data-domain-action="plan" data-tunnel="' + esc(t.tunnelId) + '" type="button">Plan</button><button class="btn" data-domain-action="verify" data-tunnel="' + esc(t.tunnelId) + '" type="button">Verify</button><button class="btn danger" data-domain-action="clear" data-tunnel="' + esc(t.tunnelId) + '" type="button">Clear</button></div></div>' +
      '</div>';
    }

    function configPanel(t) {
      const cfgDomain = safeHost(t.customDomain);
      const yaml = 'version: v1\ntunnels:\n  - name: ' + (t.tunnelId || "web") + '\n    localPort: ' + (t.localPort || "3000") + '\n    protocol: ' + (t.protocol || "https") + (cfgDomain ? '\n    domain: ' + cfgDomain : '') + '\n    readyTimeout: 90s';
      return '<pre class="yaml">' + esc(yaml) + '</pre><div class="modal-actions"><button class="btn" data-copy="' + esc(yaml) + '">Copy YAML</button><button class="btn primary" data-open-apply="' + esc(t.tunnelId) + '">Open in Apply YAML</button></div>';
    }

    function group(titleText, rows) {
      return '<div class="inspect-group"><div class="group-title">' + esc(titleText) + '</div><div class="kv">' +
        rows.map(row => {
          const label = row[0], value = row[1], isMono = row[2], truth = row.length > 3 ? row[3] : null;
          const rendered = truth === null ? esc(value) : (value === "Unknown" ? '<span class="muted">Unknown</span>' : yes(truth));
          return '<div class="kv-row"><span>' + esc(label) + '</span><span class="value ' + (isMono ? "mono" : "") + '">' + rendered + '</span><button class="copy" data-copy="' + esc(value) + '">' + copyIcon + '</button></div>';
        }).join("") +
      '</div></div>';
    }

    function line(time, level, msg) {
      return '<div class="log-line"><span class="log-time">' + esc(time) + '</span><span class="level ' + (level === "WARN" ? "warn" : level === "DEBUG" ? "debug" : "") + '">' + esc(level) + '</span><span>' + esc(msg) + '</span></div>';
    }

    function small(label, value) {
      return '<div class="small-card"><div class="label">' + esc(label) + '</div><div class="value">' + esc(value) + '</div></div>';
    }

    function isLive(status) {
      return status === "active" || status === "running" || status === "connecting";
    }

    function title(value) {
      value = String(value || "");
      return value ? value.charAt(0).toUpperCase() + value.slice(1) : "-";
    }

    function shortDate(value) {
      if (!value) return "-";
      return value.replace(/^.*?(\d{2}-\d{2})T/, "$1 ").replace(/:\d{2}(?:[+-].*)?$/, "");
    }

    function pulseIcon() {
      return '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M3 12h4l2-6 4 12 2-6h6"/></svg>';
    }
    function swapIcon() {
      return '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M7 7h13l-3-3"/><path d="M17 17H4l3 3"/><path d="M20 7l-3 3"/><path d="M4 17l3-3"/></svg>';
    }
    function warnIcon() {
      return '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M12 4 3 20h18L12 4Z"/><path d="M12 9v5"/><path d="M12 17h.01"/></svg>';
    }
    function serverIcon() {
      return '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><rect x="4" y="4" width="16" height="6" rx="1"/><rect x="4" y="14" width="16" height="6" rx="1"/><path d="M8 7h.01"/><path d="M8 17h.01"/></svg>';
    }
    function plugIcon() {
      return '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M9 7v5"/><path d="M15 7v5"/><path d="M7 12h10v2a5 5 0 0 1-10 0v-2Z"/><path d="M12 19v3"/></svg>';
    }

    function wire() {
      document.querySelectorAll("[data-menu]").forEach(el => {
        el.onclick = (event) => {
          event.stopPropagation();
          toggleContextMenu(el.getAttribute("data-menu") || "");
        };
      });
      document.querySelectorAll("[data-select]").forEach(el => {
        el.onclick = () => {
          selectedTunnel = el.getAttribute("data-select") || "";
          renderTable(snapshot);
          renderInspect();
          renderTab();
          wire();
        };
      });
      document.querySelectorAll("[data-tab]").forEach(el => {
        el.onclick = () => {
          activeTab = el.getAttribute("data-tab") || "logs";
          renderTab();
          wire();
        };
      });
      document.querySelectorAll("[data-copy]").forEach(el => {
        el.onclick = (event) => {
          event.stopPropagation();
          copyText(el.getAttribute("data-copy") || "");
        };
      });
      document.querySelectorAll("[data-action]").forEach(el => {
        el.onclick = () => runTunnelAction(el.getAttribute("data-action") || "", el.getAttribute("data-tunnel") || "");
      });
      document.querySelectorAll("[data-domain-action]").forEach(el => {
        el.onclick = () => runDomainAction(el.getAttribute("data-domain-action") || "", el.getAttribute("data-tunnel") || "");
      });
      document.querySelectorAll("[data-policy-set]").forEach(el => {
        el.onclick = () => runPolicySet(el.getAttribute("data-policy-set") || "");
      });
      document.querySelectorAll("[data-share-rotate]").forEach(el => {
        el.onclick = () => runShareRotate(el.getAttribute("data-share-rotate") || "");
      });
      document.querySelectorAll("[data-rotate-secret]").forEach(el => {
        el.onclick = () => runServerSecretRotate(el.getAttribute("data-rotate-secret") || "");
      });
      document.querySelectorAll("[data-open-apply]").forEach(el => {
        el.onclick = () => openApplyModal(configYAMLFor(tunnelByID(el.getAttribute("data-open-apply") || "") || selected()));
      });
    }

    async function apiFetch(path, options = {}) {
      const headers = Object.assign({ "X-Sealtun-Dashboard-Token": dashboardToken }, options.headers || {});
      if (options.body && !headers["Content-Type"]) headers["Content-Type"] = "application/json";
      const res = await fetch(path, Object.assign({ cache: "no-store", headers }, options));
      const payload = await res.json().catch(() => ({}));
      if (!res.ok || payload.ok === false) throw new Error(payload.error || ("Dashboard API returned " + res.status));
      return payload.data;
    }

    function postJSON(path, body) {
      return apiFetch(path, { method: "POST", body: JSON.stringify(body || {}) });
    }

    function setLiveState(mode) {
      liveMode = mode;
      const el = document.getElementById("live-state");
      if (!el) return;
      const label = mode === "live" ? "Live" : mode === "reconnecting" ? "Reconnecting" : mode === "disconnected" ? "Disconnected" : "Polling";
      el.className = "live-badge " + (mode === "live" ? "" : mode);
      el.innerHTML = '<span class="dot round"></span>' + label;
    }

    function startPolling() {
      if (pollingTimer) return;
      setLiveState("polling");
      pollingTimer = setInterval(() => refresh().catch(() => setLiveState("disconnected")), 15000);
    }

    function stopPolling() {
      if (!pollingTimer) return;
      clearInterval(pollingTimer);
      pollingTimer = null;
    }

    async function startWatch() {
      if (!dashboardToken || !window.ReadableStream) {
        startPolling();
        return;
      }
      const generation = ++watchGeneration;
      if (watchAbort) watchAbort.abort();
      watchAbort = new AbortController();
      setLiveState("reconnecting");
      try {
        const res = await fetch("/api/watch", {
          cache: "no-store",
          headers: { "X-Sealtun-Dashboard-Token": dashboardToken },
          signal: watchAbort.signal
        });
        if (!res.ok || !res.body) throw new Error("watch returned " + res.status);
        stopPolling();
        setLiveState("live");
        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        while (generation === watchGeneration) {
          const next = await reader.read();
          if (next.done) break;
          buffer += decoder.decode(next.value, { stream: true });
          let idx;
          while ((idx = buffer.indexOf("\n\n")) >= 0) {
            const frame = buffer.slice(0, idx);
            buffer = buffer.slice(idx + 2);
            handleWatchFrame(frame);
          }
        }
        if (generation === watchGeneration) throw new Error("watch stream ended");
      } catch (err) {
        if (watchAbort?.signal.aborted || generation !== watchGeneration) return;
        setLiveState("reconnecting");
        startPolling();
        setTimeout(() => {
          if (generation === watchGeneration) startWatch();
        }, 5000);
      }
    }

    function handleWatchFrame(frame) {
      const dataLines = frame.split("\n").filter(line => line.startsWith("data:")).map(line => line.slice(5).trim());
      if (!dataLines.length) return;
      try {
        const event = JSON.parse(dataLines.join("\n"));
        if (event.type === "summary" && event.data) render(event.data);
      } catch (_) {}
    }

    function confirmPayload(action, target, label, command) {
      const confirm = action + ":" + target;
      const commandLine = command ? "\n\nCLI command:\n" + command : "";
      if (!window.confirm((label || "Run action") + commandLine + "\n\nConfirm: " + confirm)) return "";
      return confirm;
    }

    function shellArg(value) {
      value = String(value ?? "");
      if (/^[A-Za-z0-9@%_+=:,./-]+$/.test(value) && value !== "") return value;
      return "'" + value.replace(/'/g, "'\"'\"'") + "'";
    }

    function exposeCommandFromForm() {
      const port = Number(document.getElementById("new-port")?.value || 0);
      const protocol = document.getElementById("new-protocol")?.value || "https";
      const domain = document.getElementById("new-domain")?.value.trim() || "";
      const basicAuth = document.getElementById("new-basic-auth")?.value.trim() || "";
      const bearer = document.getElementById("new-bearer")?.value.trim() || "";
      const allow = document.getElementById("new-allow")?.value.trim() || "";
      const deny = document.getElementById("new-deny")?.value.trim() || "";
      const tempToken = document.getElementById("new-temp-token")?.value.trim() || "";
      const tempTTL = document.getElementById("new-temp-ttl")?.value.trim() || "";
      const rateLimit = document.getElementById("new-rate-limit")?.value.trim() || "";
      const auditEnabled = document.getElementById("new-audit")?.checked || false;
      const parts = ["sealtun", "expose", String(port || "<port>")];
      if (protocol !== "https") parts.push("--protocol", protocol);
      if (domain) parts.push("--domain", domain, "--wait-domain");
      if (protocol === "https") {
        if (basicAuth) parts.push("--basic-auth", basicAuth);
        if (bearer) parts.push("--bearer-token", bearer);
        if (allow) parts.push("--ip-allowlist", allow);
        if (deny) parts.push("--ip-denylist", deny);
        if (tempToken) parts.push("--temporary-access-token", tempToken);
        if (tempTTL) parts.push("--temporary-access-ttl", tempTTL);
        if (rateLimit) parts.push("--rate-limit", rateLimit);
        if (auditEnabled) parts.push("--audit");
      }
      return parts.map(shellArg).join(" ");
    }

    function tunnelCommand(action, tunnelID) {
      return "sealtun " + action + " " + shellArg(tunnelID);
    }

    function domainCommand(action, tunnelID, domain, wait) {
      if (action === "plan") return "sealtun domain plan " + shellArg(tunnelID) + " " + shellArg(domain || "<domain>");
      if (action === "add") return "sealtun domain add " + shellArg(tunnelID) + " " + shellArg(domain || "<domain>") + (wait ? " --wait" : "");
      if (action === "verify") return "sealtun domain verify " + shellArg(tunnelID) + (wait ? " --wait" : "");
      if (action === "clear") return "sealtun domain clear " + shellArg(tunnelID);
      return "";
    }

    function policySetCommand(tunnelID) {
      const rateLimit = document.getElementById("policy-rate-limit")?.value.trim() || "";
      const previousRateLimit = tabCache(tunnelByID(tunnelID) || selected(), "audit")?.policy?.rateLimit || "";
      const audit = document.getElementById("policy-audit-enabled")?.checked;
      const parts = ["sealtun", "policy", "set", tunnelID];
      if (rateLimit) parts.push("--rate-limit", rateLimit);
      else if (previousRateLimit) parts.push("--clear-rate-limit");
      parts.push(audit ? "--audit" : "--no-audit");
      return parts.map(shellArg).join(" ");
    }

    function shareRotateCommand(tunnelID, name, ttl) {
      const parts = ["sealtun", "share", "rotate", tunnelID, name || "<name>"];
      if (ttl) parts.push("--ttl", ttl);
      return parts.map(shellArg).join(" ");
    }

    function serverSecretRotateCommand(tunnelID) {
      return "sealtun rotate " + shellArg(tunnelID) + " --server-secret";
    }

    async function runTunnelAction(action, tunnelID) {
      if (!action || !tunnelID) return;
      const confirm = confirmPayload(action, tunnelID, title(action) + " tunnel " + tunnelID, tunnelCommand(action, tunnelID));
      if (!confirm) return;
      try {
        await postJSON("/api/tunnels/" + encodeURIComponent(tunnelID) + "/" + action, { confirm });
        showToast(title(action) + " completed");
        tabDataCache = {};
        await refresh();
      } catch (err) {
        showToast(err.message || String(err), true);
      }
    }

    async function runDomainAction(action, tunnelID) {
      if (!action || !tunnelID) return;
      const path = "/api/tunnels/" + encodeURIComponent(tunnelID) + "/domain/" + action;
      let body = {};
      if (action === "add" || action === "plan") {
        const domain = window.prompt("Custom domain");
        if (!domain) return;
        body.domain = domain.trim();
        if (action === "plan") showToast("CLI: " + domainCommand(action, tunnelID, body.domain, false));
      }
      if (action === "add" || action === "clear") {
        const confirm = confirmPayload("domain-" + action, tunnelID, title(action) + " custom domain", domainCommand(action, tunnelID, body.domain, false));
        if (!confirm) return;
        body.confirm = confirm;
      }
      if (action === "verify" && window.confirm("Wait until the domain is ready?")) {
        body.wait = true;
        body.confirm = confirmPayload("domain-verify", tunnelID, "Wait for domain verification", domainCommand(action, tunnelID, "", true));
        if (!body.confirm) return;
      }
      try {
        const data = await postJSON(path, body);
        showToast("Domain " + action + " completed");
        if (data) openResultModal("Domain " + title(action), data);
        tabDataCache = {};
        await refresh();
      } catch (err) {
        showToast(err.message || String(err), true);
      }
    }

    async function runPolicySet(tunnelID) {
      if (!tunnelID) return;
      const rateLimit = document.getElementById("policy-rate-limit")?.value.trim() || "";
      const previousRateLimit = tabCache(tunnelByID(tunnelID) || selected(), "audit")?.policy?.rateLimit || "";
      const body = {
        rateLimit,
        clearRateLimit: !rateLimit && !!previousRateLimit,
        auditEnabled: document.getElementById("policy-audit-enabled")?.checked || false
      };
      const confirm = confirmPayload("policy-set", tunnelID, "Update access policy", policySetCommand(tunnelID));
      if (!confirm) return;
      body.confirm = confirm;
      try {
        const data = await postJSON("/api/tunnels/" + encodeURIComponent(tunnelID) + "/policy/set", body);
        showToast("Policy updated");
        openResultModal("Policy Updated", data);
        tabDataCache = {};
        await refresh();
      } catch (err) {
        showToast(err.message || String(err), true);
      }
    }

    async function runShareRotate(tunnelID) {
      if (!tunnelID) return;
      const name = document.getElementById("share-rotate-name")?.value.trim() || "";
      const ttl = document.getElementById("share-rotate-ttl")?.value.trim() || "1h";
      if (!name) {
        showToast("Share name is required", true);
        return;
      }
      const confirm = confirmPayload("share-rotate", tunnelID, "Rotate temporary share link", shareRotateCommand(tunnelID, name, ttl));
      if (!confirm) return;
      try {
        const data = await postJSON("/api/tunnels/" + encodeURIComponent(tunnelID) + "/share/rotate", { confirm, name, ttl });
        showToast("Share link rotated");
        openResultModal("Rotated Share Link", data);
        tabDataCache = {};
        await refresh();
      } catch (err) {
        showToast(err.message || String(err), true);
      }
    }

    async function runServerSecretRotate(tunnelID) {
      if (!tunnelID) return;
      const confirm = confirmPayload("rotate-server-secret", tunnelID, "Rotate tunnel credential", serverSecretRotateCommand(tunnelID));
      if (!confirm) return;
      try {
        const data = await postJSON("/api/tunnels/" + encodeURIComponent(tunnelID) + "/rotate/server-secret", { confirm });
        showToast("Tunnel credential rotated");
        openResultModal("Tunnel Credential Rotated", data);
        tabDataCache = {};
        await refresh();
      } catch (err) {
        showToast(err.message || String(err), true);
      }
    }

    function configYAMLFor(t) {
      if (!t) return "version: v1\ntunnels:\n  - name: web\n    localPort: 3000\n    protocol: https\n";
      const cfgDomain = safeHost(t.customDomain);
      return "version: v1\ntunnels:\n  - name: " + (t.tunnelId || "web") + "\n    localPort: " + (t.localPort || "3000") + "\n    protocol: " + (t.protocol || "https") + (cfgDomain ? "\n    domain: " + cfgDomain : "") + "\n    readyTimeout: 90s\n";
    }

    function modal(titleText, bodyHTML, actionsHTML) {
      const backdrop = document.getElementById("modal-backdrop");
      backdrop.innerHTML =
        '<div class="modal" role="dialog" aria-modal="true">' +
          '<div class="modal-head"><div class="modal-title">' + esc(titleText) + '</div><button class="close" data-modal-close type="button">x</button></div>' +
          '<div class="modal-body">' + bodyHTML + '</div>' +
          '<div class="modal-actions">' + (actionsHTML || '<button class="btn" data-modal-close type="button">Close</button>') + '</div>' +
        '</div>';
      backdrop.hidden = false;
      backdrop.querySelectorAll("[data-modal-close]").forEach(btn => btn.onclick = closeModal);
      return backdrop;
    }

    function closeModal() {
      const backdrop = document.getElementById("modal-backdrop");
      backdrop.hidden = true;
      backdrop.innerHTML = "";
    }

    function openResultModal(titleText, data) {
      modal(titleText, '<pre class="result-box">' + esc(JSON.stringify(data, null, 2)) + '</pre>');
    }

    function openNewTunnelModal(template = "https") {
      const defaults = protocolDefaults[template] || protocolDefaults.https;
      const body =
        '<div class="template-pills">' + Object.keys(protocolDefaults).map(key => '<button class="pill" data-template="' + esc(key) + '" type="button">' + esc(key.toUpperCase()) + '</button>').join("") + '</div>' +
        '<div><button class="btn" id="discover-ports" type="button">Discover local ports</button></div>' +
        '<div class="discover-list" id="discover-results"></div>' +
        '<div class="form-grid">' +
          '<label class="field">Name<input class="input" id="new-name" value="' + esc(defaults.name) + '"></label>' +
          '<label class="field">Protocol<select class="select" id="new-protocol"><option value="https">https</option><option value="ssh">ssh</option><option value="tcp">tcp</option></select></label>' +
          '<label class="field">Local Port<input class="input" id="new-port" type="number" min="1" max="65535" value="' + esc(defaults.port) + '"></label>' +
          '<label class="field" data-http-field>Domain<input class="input" id="new-domain" placeholder="app.example.com"></label>' +
          '<label class="field" data-http-field>Basic Auth<input class="input" id="new-basic-auth" placeholder="user:password"></label>' +
          '<label class="field" data-http-field>Bearer Token<input class="input" id="new-bearer" placeholder="optional"></label>' +
          '<label class="field" data-http-field>IP Allowlist<input class="input" id="new-allow" placeholder="1.2.3.4, 10.0.0.0/8"></label>' +
          '<label class="field" data-http-field>IP Denylist<input class="input" id="new-deny" placeholder="optional"></label>' +
          '<label class="field" data-http-field>Temporary Token<input class="input" id="new-temp-token" placeholder="optional"></label>' +
          '<label class="field" data-http-field>Temporary TTL<input class="input" id="new-temp-ttl" placeholder="1h"></label>' +
          '<label class="field" data-http-field>Rate Limit<input class="input" id="new-rate-limit" placeholder="60/m"></label>' +
          '<label class="field" data-http-field>Audit<label class="checkline"><input id="new-audit" type="checkbox"> Enable access audit</label></label>' +
        '</div>' +
        '<div class="command-preview" id="new-command">sealtun expose 3000</div>' +
        '<pre class="result-box" id="new-result">Ready.</pre>';
      const backdrop = modal("New Tunnel", body, '<button class="btn" data-modal-close type="button">Cancel</button><button class="btn primary" id="create-tunnel" type="button">Create</button>');
      const updateProtocolFields = () => {
        const isHTTPS = document.getElementById("new-protocol").value === "https";
        backdrop.querySelectorAll("[data-http-field] input").forEach(input => {
          input.disabled = !isHTTPS;
          if (!isHTTPS && input.type === "checkbox") input.checked = false;
          if (!isHTTPS && input.type !== "checkbox") input.value = "";
        });
        document.getElementById("new-command").textContent = exposeCommandFromForm();
      };
      const setTemplate = (key) => {
        const item = protocolDefaults[key] || protocolDefaults.https;
        document.getElementById("new-name").value = item.name;
        document.getElementById("new-port").value = item.port;
        document.getElementById("new-protocol").value = item.protocol;
        updateProtocolFields();
      };
      setTemplate(template);
      backdrop.querySelectorAll("[data-template]").forEach(btn => btn.onclick = () => setTemplate(btn.getAttribute("data-template")));
      document.getElementById("discover-ports").onclick = async () => {
        const target = document.getElementById("discover-results");
        target.innerHTML = '<div class="context-note">Scanning local listening TCP ports...</div>';
        try {
          const items = await apiFetch("/api/discover?limit=30");
          if (!items || !items.length) {
            target.innerHTML = '<div class="context-note">No local listening TCP ports found.</div>';
            return;
          }
          target.innerHTML = items.map(item =>
            '<button class="discover-row" data-discover-port="' + esc(item.port) + '" data-discover-protocol="' + esc(item.protocolHint) + '" data-discover-template="' + esc(item.templateHint) + '" type="button">' +
              '<span class="mono">' + esc(item.port) + '</span>' +
              '<span><strong>' + esc(item.templateHint || item.protocolHint) + '</strong><div class="context-sub">' + esc((item.processName || "unknown process") + " · " + (item.address || "-")) + '</div></span>' +
              '<span class="tag">' + esc(item.protocolHint || "-") + '</span>' +
            '</button>'
          ).join("");
          target.querySelectorAll("[data-discover-port]").forEach(btn => {
            btn.onclick = () => {
              document.getElementById("new-port").value = btn.getAttribute("data-discover-port") || "";
              document.getElementById("new-protocol").value = btn.getAttribute("data-discover-protocol") || "https";
              document.getElementById("new-name").value = btn.getAttribute("data-discover-template") || "web";
              updateProtocolFields();
            };
          });
        } catch (err) {
          target.innerHTML = '<div class="context-note">' + esc(err.message || String(err)) + '</div>';
        }
      };
      ["new-name", "new-protocol", "new-port", "new-domain", "new-basic-auth", "new-bearer", "new-allow", "new-deny", "new-temp-token", "new-temp-ttl", "new-rate-limit", "new-audit"].forEach(id => {
        document.getElementById(id).oninput = updateProtocolFields;
        document.getElementById(id).onchange = updateProtocolFields;
      });
      document.getElementById("create-tunnel").onclick = async () => {
        const name = document.getElementById("new-name").value.trim();
        const target = name || "dashboard-tunnel";
        const confirm = confirmPayload("create", target, "Create tunnel", exposeCommandFromForm());
        if (!confirm) return;
        const splitList = (id) => document.getElementById(id).value.split(",").map(x => x.trim()).filter(Boolean);
        const body = {
          confirm,
          name,
          protocol: document.getElementById("new-protocol").value,
          localPort: Number(document.getElementById("new-port").value),
          domain: document.getElementById("new-domain").value.trim(),
          basicAuth: document.getElementById("new-basic-auth").value.trim(),
          bearerToken: document.getElementById("new-bearer").value.trim(),
          ipAllowlist: splitList("new-allow"),
          ipDenylist: splitList("new-deny"),
          temporaryAccessToken: document.getElementById("new-temp-token").value.trim(),
          temporaryAccessTTL: document.getElementById("new-temp-ttl").value.trim(),
          rateLimit: document.getElementById("new-rate-limit").value.trim(),
          auditEnabled: document.getElementById("new-audit").checked
        };
        try {
          const data = await postJSON("/api/tunnels", body);
          document.getElementById("new-result").textContent = JSON.stringify(data, null, 2);
          tabDataCache = {};
          await refresh();
        } catch (err) {
          document.getElementById("new-result").textContent = err.message || String(err);
        }
      };
    }

    function openApplyModal(initialYAML = "") {
      const body =
        '<label class="field full">sealtun.yaml<textarea class="textarea" id="apply-yaml">' + esc(initialYAML || "version: v1\ntunnels:\n  - name: web\n    localPort: 3000\n    protocol: https\n") + '</textarea></label>' +
        '<div class="command-preview" id="apply-command">sealtun apply -f sealtun.yaml --dry-run</div>' +
        '<pre class="result-box" id="apply-result">Ready.</pre>';
      modal("Apply YAML", body, '<button class="btn" data-modal-close type="button">Close</button><button class="btn" id="apply-dry-run" type="button">Dry Run</button><button class="btn" id="apply-diff" type="button">Diff</button><button class="btn primary" id="apply-run" type="button">Apply</button>');
      const run = async (kind) => {
        const yaml = document.getElementById("apply-yaml").value;
        const body = { yaml };
        let path = "/api/apply/" + kind;
        const command = kind === "dry-run" ? "sealtun apply -f sealtun.yaml --dry-run" : (kind === "diff" ? "sealtun diff -f sealtun.yaml" : "sealtun apply -f sealtun.yaml");
        document.getElementById("apply-command").textContent = command;
        if (kind === "apply") {
          path = "/api/apply";
          body.confirm = confirmPayload("apply", "dashboard-yaml", "Apply YAML", command);
          if (!body.confirm) return;
        }
        try {
          const data = await postJSON(path, body);
          document.getElementById("apply-result").textContent = JSON.stringify(data, null, 2);
          if (kind === "apply") {
            tabDataCache = {};
            await refresh();
          }
        } catch (err) {
          document.getElementById("apply-result").textContent = err.message || String(err);
        }
      };
      document.getElementById("apply-dry-run").onclick = () => run("dry-run");
      document.getElementById("apply-diff").onclick = () => run("diff");
      document.getElementById("apply-run").onclick = () => run("apply");
    }

    async function copyText(text) {
      if (!text) return;
      try {
        await navigator.clipboard.writeText(text);
        showToast("Copied: " + text);
      } catch (_) {}
    }

    async function refresh() {
      if (refreshInFlight) return refreshInFlight;
      const button = document.getElementById("refresh-btn");
      button.disabled = true;
      contextSnapshot = null;
      refreshInFlight = (async () => {
      const res = await fetch("/api/summary", { cache: "no-store", headers: { "X-Sealtun-Dashboard-Token": dashboardToken } });
      if (!res.ok) throw new Error("Dashboard API returned " + res.status);
      render(await res.json());
      })();
      try {
        await refreshInFlight;
      } finally {
        refreshInFlight = null;
        button.disabled = false;
      }
    }

    document.getElementById("refresh-btn").onclick = refresh;
    document.getElementById("new-tunnel-btn").onclick = () => openNewTunnelModal();
    document.getElementById("apply-yaml-btn").onclick = () => openApplyModal(configYAMLFor(selected()));
    document.addEventListener("click", event => {
      if (!event.target.closest(".context-wrap")) closeContextMenus();
    });
    refresh().catch(err => {
      document.getElementById("tunnel-table").innerHTML = '<div class="empty"><div><strong>Dashboard failed to load</strong><div>' + esc(err.message) + '</div></div></div>';
    });
    startWatch();
  </script>
</body>
</html>`))
