package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labring/sealtun/pkg/auth"
	"github.com/labring/sealtun/pkg/k8s"
	tunnelprotocol "github.com/labring/sealtun/pkg/protocol"
	"github.com/labring/sealtun/pkg/session"
)

type dashboardAPIResponse struct {
	OK      bool        `json:"ok"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type dashboardConfirmRequest struct {
	Confirm string `json:"confirm"`
}

type dashboardTunnelCreateRequest struct {
	Confirm              string   `json:"confirm"`
	Name                 string   `json:"name,omitempty"`
	Protocol             string   `json:"protocol"`
	LocalPort            int      `json:"localPort"`
	Domain               string   `json:"domain,omitempty"`
	WaitDomain           bool     `json:"waitDomain,omitempty"`
	ReadyTimeout         string   `json:"readyTimeout,omitempty"`
	DomainTimeout        string   `json:"domainTimeout,omitempty"`
	BasicAuthCredential  string   `json:"basicAuth,omitempty"`
	BasicAuthUser        string   `json:"basicAuthUser,omitempty"`
	BasicAuthPassword    string   `json:"basicAuthPassword,omitempty"`
	BearerToken          string   `json:"bearerToken,omitempty"`
	IPAllowlist          []string `json:"ipAllowlist,omitempty"`
	IPDenylist           []string `json:"ipDenylist,omitempty"`
	TemporaryAccessToken string   `json:"temporaryAccessToken,omitempty"`
	TemporaryAccessTTL   string   `json:"temporaryAccessTTL,omitempty"`
}

type dashboardApplyRequest struct {
	Confirm string `json:"confirm,omitempty"`
	YAML    string `json:"yaml"`
}

type dashboardDomainRequest struct {
	Confirm string `json:"confirm,omitempty"`
	Domain  string `json:"domain,omitempty"`
	Wait    bool   `json:"wait,omitempty"`
	Timeout string `json:"timeout,omitempty"`
}

type dashboardWatchEvent struct {
	Type string           `json:"type"`
	Data dashboardPayload `json:"data"`
}

var dashboardPortDiscoverer portDiscoverer = systemPortDiscoverer{}

func (s dashboardServer) serveAPI(w http.ResponseWriter, r *http.Request) {
	if !s.requireToken(w, r) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	switch {
	case r.Method == http.MethodGet && path == "watch":
		s.serveWatch(w, r)
	case r.Method == http.MethodGet && path == "discover":
		s.serveDiscover(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "tunnels/"):
		s.serveTunnelReadAPI(w, r, strings.TrimPrefix(path, "tunnels/"))
	case r.Method == http.MethodPost && path == "tunnels":
		s.serveCreateTunnel(w, r)
	case r.Method == http.MethodPost && path == "apply/dry-run":
		s.serveApplyYAML(w, r, true, false)
	case r.Method == http.MethodPost && path == "apply/diff":
		s.serveApplyYAML(w, r, false, true)
	case r.Method == http.MethodPost && path == "apply":
		s.serveApplyYAML(w, r, false, false)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "tunnels/"):
		s.serveTunnelMutationAPI(w, r, strings.TrimPrefix(path, "tunnels/"))
	default:
		writeDashboardError(w, http.StatusNotFound, "dashboard API route not found")
	}
}

func (s dashboardServer) serveTunnelReadAPI(w http.ResponseWriter, r *http.Request, rest string) {
	tunnelID, action, ok := splitDashboardTunnelAction(rest)
	if !ok {
		writeDashboardError(w, http.StatusNotFound, "dashboard tunnel API route not found")
		return
	}
	switch action {
	case "logs":
		payload, err := dashboardTunnelLogs(r.Context(), tunnelID, r)
		writeDashboardResult(w, "logs loaded", payload, err)
	case "metrics":
		sess, err := dashboardScopedSession(tunnelID)
		if err != nil {
			writeDashboardResult(w, "metrics loaded", nil, err)
			return
		}
		payload, err := collectMetricsPayloadForSession(r.Context(), *sess)
		writeDashboardResult(w, "metrics loaded", payload, err)
	case "events":
		sess, err := dashboardScopedSession(tunnelID)
		if err != nil {
			writeDashboardResult(w, "events loaded", nil, err)
			return
		}
		payload, err := collectEventsPayloadForSession(r.Context(), *sess, 8*time.Second)
		writeDashboardResult(w, "events loaded", payload, err)
	case "resources":
		payload, err := dashboardTunnelResources(r.Context(), tunnelID)
		writeDashboardResult(w, "resources loaded", payload, err)
	default:
		writeDashboardError(w, http.StatusNotFound, "dashboard tunnel API route not found")
	}
}

func (s dashboardServer) serveTunnelMutationAPI(w http.ResponseWriter, r *http.Request, rest string) {
	tunnelID, action, ok := splitDashboardTunnelAction(rest)
	if !ok {
		writeDashboardError(w, http.StatusNotFound, "dashboard tunnel API route not found")
		return
	}
	switch action {
	case "start":
		req, err := readDashboardJSON[dashboardConfirmRequest](r, 4096)
		if err != nil {
			writeDashboardError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !requireDashboardConfirm(w, req.Confirm, "start", tunnelID) {
			return
		}
		err = startTunnelByID(r.Context(), tunnelID)
		writeDashboardResult(w, fmt.Sprintf("started tunnel %s", tunnelID), nil, err)
	case "stop":
		req, err := readDashboardJSON[dashboardConfirmRequest](r, 4096)
		if err != nil {
			writeDashboardError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !requireDashboardConfirm(w, req.Confirm, "stop", tunnelID) {
			return
		}
		err = stopTunnelByID(r.Context(), tunnelID)
		writeDashboardResult(w, fmt.Sprintf("stopped tunnel %s", tunnelID), nil, err)
	case "cleanup":
		req, err := readDashboardJSON[dashboardConfirmRequest](r, 4096)
		if err != nil {
			writeDashboardError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !requireDashboardConfirm(w, req.Confirm, "cleanup", tunnelID) {
			return
		}
		err = cleanupTunnelByID(r.Context(), tunnelID)
		writeDashboardResult(w, fmt.Sprintf("cleaned up tunnel %s", tunnelID), nil, err)
	case "domain/plan", "domain/add", "domain/clear", "domain/verify":
		s.serveDomainAPI(w, r, tunnelID, strings.TrimPrefix(action, "domain/"))
	default:
		writeDashboardError(w, http.StatusNotFound, "dashboard tunnel API route not found")
	}
}

func (s dashboardServer) serveCreateTunnel(w http.ResponseWriter, r *http.Request) {
	req, err := readDashboardJSON[dashboardTunnelCreateRequest](r, 64*1024)
	if err != nil {
		writeDashboardError(w, http.StatusBadRequest, err.Error())
		return
	}
	target := "dashboard-tunnel"
	if strings.TrimSpace(req.Name) != "" {
		target = strings.TrimSpace(req.Name)
	}
	if !requireDashboardConfirm(w, req.Confirm, "create", target) {
		return
	}
	result, err := createDashboardTunnel(r.Context(), req)
	writeDashboardResult(w, "created tunnel "+result.TunnelID, result, err)
}

func (s dashboardServer) serveApplyYAML(w http.ResponseWriter, r *http.Request, dryRun bool, diff bool) {
	req, err := readDashboardJSON[dashboardApplyRequest](r, applyFileMaxBytes+4096)
	if err != nil {
		writeDashboardError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len([]byte(req.YAML)) > applyFileMaxBytes {
		writeDashboardError(w, http.StatusBadRequest, fmt.Sprintf("apply YAML is too large; limit is %d bytes", applyFileMaxBytes))
		return
	}
	if diff {
		results, err := runDashboardDiffContent([]byte(req.YAML))
		writeDashboardResult(w, "diff completed", results, err)
		return
	}
	if !dryRun && !requireDashboardConfirm(w, req.Confirm, "apply", "dashboard-yaml") {
		return
	}
	results, err := runDashboardApplyContent(r.Context(), []byte(req.YAML), dryRun)
	message := "dry run completed"
	if !dryRun {
		message = "apply completed"
	}
	writeDashboardResult(w, message, results, err)
}

func (s dashboardServer) serveDomainAPI(w http.ResponseWriter, r *http.Request, tunnelID, action string) {
	req, err := readDashboardJSON[dashboardDomainRequest](r, 16*1024)
	if err != nil {
		writeDashboardError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := dashboardScopedSession(tunnelID); err != nil {
		writeDashboardResult(w, "domain "+action, nil, err)
		return
	}
	switch action {
	case "plan":
		domain, err := validateDashboardDomain(req.Domain)
		if err != nil {
			writeDashboardError(w, http.StatusBadRequest, err.Error())
			return
		}
		payload, err := planSessionCustomDomain(tunnelID, domain)
		writeDashboardResult(w, "domain plan generated", payload, err)
	case "add":
		domain, err := validateDashboardDomain(req.Domain)
		if err != nil {
			writeDashboardError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !requireDashboardConfirm(w, req.Confirm, "domain-add", tunnelID) {
			return
		}
		timeout, err := parseDashboardTimeout(req.Timeout, 5*time.Minute)
		if err != nil {
			writeDashboardError(w, http.StatusBadRequest, err.Error())
			return
		}
		payload, err := configureSessionCustomDomain(r.Context(), tunnelID, domain)
		data := map[string]interface{}{"domain": payload}
		if err == nil && req.Wait {
			verify, waitErr := waitForSessionDomain(r.Context(), tunnelID, timeout)
			data["verify"] = verify
			err = waitErr
		}
		writeDashboardResult(w, "domain attached", data, err)
	case "clear":
		if !requireDashboardConfirm(w, req.Confirm, "domain-clear", tunnelID) {
			return
		}
		payload, err := clearSessionCustomDomain(r.Context(), tunnelID)
		writeDashboardResult(w, "domain cleared", payload, err)
	case "verify":
		timeout, err := parseDashboardTimeout(req.Timeout, 5*time.Minute)
		if err != nil {
			writeDashboardError(w, http.StatusBadRequest, err.Error())
			return
		}
		var payload *domainVerifyPayload
		if req.Wait {
			if !requireDashboardConfirm(w, req.Confirm, "domain-verify", tunnelID) {
				return
			}
			payload, err = waitForSessionDomain(r.Context(), tunnelID, timeout)
		} else {
			payload, err = verifySessionDomain(r.Context(), tunnelID)
		}
		writeDashboardResult(w, "domain verification completed", payload, err)
	default:
		writeDashboardError(w, http.StatusNotFound, "dashboard domain API route not found")
	}
}

func splitDashboardTunnelAction(rest string) (string, string, bool) {
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func requireDashboardConfirm(w http.ResponseWriter, got, action, target string) bool {
	want := action + ":" + target
	if got == want {
		return true
	}
	writeDashboardError(w, http.StatusBadRequest, fmt.Sprintf("confirmation required: %s", want))
	return false
}

func readDashboardJSON[T any](r *http.Request, limit int64) (T, error) {
	var out T
	if r.Body == nil {
		return out, fmt.Errorf("request body is required")
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return out, err
	}
	if int64(len(body)) > limit {
		return out, fmt.Errorf("request body exceeds %d bytes", limit)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return out, fmt.Errorf("request body is required")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, err
	}
	var extra interface{}
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return out, err
		}
		return out, fmt.Errorf("request body must contain a single JSON object")
	}
	return out, nil
}

func writeDashboardResult(w http.ResponseWriter, message string, data interface{}, err error) {
	if err != nil {
		writeDashboardError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeDashboardJSON(w, http.StatusOK, dashboardAPIResponse{OK: true, Message: message, Data: data})
}

func writeDashboardError(w http.ResponseWriter, status int, message string) {
	writeDashboardJSON(w, status, dashboardAPIResponse{OK: false, Error: message})
}

func writeDashboardJSON(w http.ResponseWriter, status int, payload dashboardAPIResponse) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

func (s dashboardServer) serveWatch(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeDashboardError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")

	write := func() bool {
		payload := dashboardWatchEvent{Type: "summary", Data: collectDashboardPayload(r.Context())}
		data, err := json.Marshal(payload)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: summary\ndata: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !write() {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !write() {
				return
			}
		}
	}
}

func (s dashboardServer) serveDiscover(w http.ResponseWriter, r *http.Request) {
	limit, err := parseDiscoverLimit(r.URL.Query().Get("limit"), 30)
	if err != nil {
		writeDashboardError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := discoverLocalPorts(r.Context(), discoverOptions{Limit: limit, Protocol: "auto"}, dashboardPortDiscoverer)
	writeDashboardResult(w, "local ports discovered", items, err)
}

func dashboardTunnelLogs(ctx context.Context, tunnelID string, r *http.Request) (map[string]string, error) {
	tail, err := parseDashboardTail(r.URL.Query().Get("tail"))
	if err != nil {
		return nil, err
	}
	since, err := parseDashboardSince(r.URL.Query().Get("since"))
	if err != nil {
		return nil, err
	}
	sess, err := dashboardScopedSession(tunnelID)
	if err != nil {
		return nil, err
	}
	client, err := k8sClientForSession(*sess)
	if err != nil {
		return nil, err
	}
	logCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var buf bytes.Buffer
	opts := k8s.TunnelLogOptions{TailLines: tail}
	if since > 0 {
		opts.SinceSeconds = int64(since.Seconds())
	}
	if err := client.WithNamespace(sess.Namespace).StreamTunnelLogs(logCtx, sess.TunnelID, &buf, opts); err != nil {
		return nil, fmt.Errorf("stream logs for tunnel %s: %w", sess.TunnelID, err)
	}
	return map[string]string{"text": buf.String()}, nil
}

func dashboardTunnelResources(ctx context.Context, tunnelID string) (*k8s.TunnelResourceList, error) {
	sess, err := dashboardScopedSession(tunnelID)
	if err != nil {
		return nil, err
	}
	client, err := k8sClientForSession(*sess)
	if err != nil {
		return nil, err
	}
	resourceCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return client.WithNamespace(sess.Namespace).TunnelResources(resourceCtx, sess.TunnelID)
}

func dashboardScopedSession(tunnelID string) (*session.TunnelSession, error) {
	sess, err := findSession(tunnelID)
	if err != nil {
		return nil, err
	}
	scope, err := dashboardActiveScope()
	if err != nil {
		return nil, err
	}
	if sess.Region != scope.region || sess.Namespace != scope.namespace {
		return nil, fmt.Errorf("tunnel %s is outside the active dashboard scope", sess.TunnelID)
	}
	return sess, nil
}

func parseDashboardTail(value string) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return 200, nil
	}
	tail, err := strconv.ParseInt(value, 10, 64)
	if err != nil || tail < 0 {
		return 0, fmt.Errorf("tail must be between 0 and 1000")
	}
	if tail > 1000 {
		return 0, fmt.Errorf("tail must be between 0 and 1000")
	}
	return tail, nil
}

func parseDashboardSince(value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	since, err := time.ParseDuration(value)
	if err != nil || since < 0 {
		return 0, fmt.Errorf("since must be a non-negative duration")
	}
	return since, nil
}

func parseDashboardTimeout(value string, fallback time.Duration) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return fallback, nil
	}
	timeout, err := time.ParseDuration(value)
	if err != nil || timeout <= 0 {
		return 0, fmt.Errorf("timeout must be greater than 0")
	}
	return timeout, nil
}

func validateDashboardDomain(value string) (string, error) {
	domain, err := validateCustomDomain(value)
	if err != nil {
		return "", err
	}
	if domain == "" {
		return "", fmt.Errorf("custom domain is required")
	}
	return domain, nil
}

func startTunnelByID(ctx context.Context, tunnelID string) error {
	sess, err := dashboardScopedSession(tunnelID)
	if err != nil {
		return err
	}
	if sess.Secret == "" {
		return fmt.Errorf("tunnel %s cannot be started because its local secret is unavailable; run cleanup and recreate the tunnel", sess.TunnelID)
	}
	if sessionExpired(*sess, time.Now()) {
		return fmt.Errorf("tunnel %s has expired; run cleanup and recreate the tunnel", sess.TunnelID)
	}
	return startTunnelSession(ctx, sess)
}

func stopTunnelByID(ctx context.Context, tunnelID string) error {
	sess, err := dashboardScopedSession(tunnelID)
	if err != nil {
		return err
	}
	pauseCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := pauseSessionResources(pauseCtx, *sess); err != nil {
		return fmt.Errorf("pause tunnel %s: %w", sess.TunnelID, err)
	}
	sess.PID = 0
	sess.ConnectionState = session.ConnectionStateStopped
	sess.LastError = ""
	if err := session.Update(*sess); err != nil {
		return fmt.Errorf("update local session %s: %w", sess.TunnelID, err)
	}
	return nil
}

func cleanupTunnelByID(ctx context.Context, tunnelID string) error {
	sess, err := dashboardScopedSession(tunnelID)
	if err != nil {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, tunnelCleanupTimeout)
	defer cancel()
	if err := cleanupSessionResources(cleanupCtx, *sess); err != nil {
		return err
	}
	return session.Delete(sess.TunnelID)
}

func runDashboardDiffContent(data []byte) ([]diffResult, error) {
	config, err := loadApplyData("dashboard yaml", data)
	if err != nil {
		return nil, err
	}
	scope, err := dashboardActiveScope()
	if err != nil {
		return nil, err
	}
	return runDiffConfigWithSessionLookup(config, func(tunnelID string) (*session.TunnelSession, error) {
		sess, err := session.Get(tunnelID)
		if err != nil {
			return nil, err
		}
		if sess.Region != scope.region || sess.Namespace != scope.namespace {
			return nil, fmt.Errorf("tunnel %s already belongs to region %s namespace %s; active dashboard scope is region %s namespace %s", sess.TunnelID, sess.Region, sess.Namespace, scope.region, scope.namespace)
		}
		return sess, nil
	})
}

func createDashboardTunnel(ctx context.Context, req dashboardTunnelCreateRequest) (applyResult, error) {
	authData, client, kubeconfig, err := dashboardActiveKubeClient()
	if err != nil {
		return applyResult{}, err
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "dash-" + uuid.New().String()[:8]
	}
	protocol := strings.TrimSpace(req.Protocol)
	if protocol == "" {
		protocol = tunnelprotocol.HTTPS
	}
	localPort := req.LocalPort
	if localPort == 0 {
		return applyResult{}, fmt.Errorf("localPort is required")
	}
	effectiveReadyTimeout, err := parseDashboardTimeout(req.ReadyTimeout, readyTimeout)
	if err != nil {
		return applyResult{}, err
	}
	effectiveDomainTimeout, err := parseDashboardTimeout(req.DomainTimeout, domainWaitTimeout)
	if err != nil {
		return applyResult{}, err
	}
	if strings.TrimSpace(req.TemporaryAccessTTL) != "" {
		ttl, err := time.ParseDuration(req.TemporaryAccessTTL)
		if err != nil || ttl <= 0 {
			return applyResult{}, fmt.Errorf("temporaryAccessTTL must be greater than 0")
		}
	}
	basicAuth := (*applyBasicAuth)(nil)
	if req.BasicAuthCredential != "" || req.BasicAuthUser != "" || req.BasicAuthPassword != "" {
		basicAuth = &applyBasicAuth{
			Credential: req.BasicAuthCredential,
			Username:   req.BasicAuthUser,
			Password:   req.BasicAuthPassword,
		}
	}
	policy := (*applyAccessPolicy)(nil)
	if req.BearerToken != "" || len(req.IPAllowlist) > 0 || len(req.IPDenylist) > 0 || req.TemporaryAccessToken != "" {
		policy = &applyAccessPolicy{
			BearerToken: req.BearerToken,
			IPAllowlist: append([]string(nil), req.IPAllowlist...),
			IPDenylist:  append([]string(nil), req.IPDenylist...),
		}
		if req.TemporaryAccessToken != "" {
			policy.TemporaryLinks = []applyTemporaryLink{{
				Name:  "dashboard",
				Token: req.TemporaryAccessToken,
				TTL:   valueOr(req.TemporaryAccessTTL, time.Hour.String()),
			}}
		}
	}
	result, err := applyOneTunnel(ctx, applyTunnel{
		Name:          name,
		LocalPort:     localPort,
		Protocol:      protocol,
		Domain:        req.Domain,
		WaitDomain:    req.WaitDomain,
		ReadyTimeout:  effectiveReadyTimeout.String(),
		DomainTimeout: effectiveDomainTimeout.String(),
		BasicAuth:     basicAuth,
		AccessPolicy:  policy,
	}, authData, client, kubeconfig, false)
	if err != nil {
		return result, err
	}
	if err := ensureDaemonRunning(); err != nil {
		rollbackApplyResults(client, []applyResult{result})
		return result, fmt.Errorf("failed to start local daemon: %w", err)
	}
	if err := waitForDaemonSession(result.TunnelID, daemonConnectTimeout); err != nil {
		rollbackApplyResults(client, []applyResult{result})
		return result, err
	}
	return result, nil
}

func runDashboardApplyContent(ctx context.Context, data []byte, dryRun bool) ([]applyResult, error) {
	config, err := loadApplyData("dashboard yaml", data)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return runApplyConfig(ctx, config, true)
	}
	if len(config.Tunnels) == 0 {
		return nil, fmt.Errorf("apply file has no tunnels")
	}
	if err := validateApplyTunnelNames(config.Tunnels); err != nil {
		return nil, err
	}
	authData, client, kubeconfig, err := dashboardActiveKubeClient()
	if err != nil {
		return nil, err
	}
	results := make([]applyResult, 0, len(config.Tunnels))
	for _, item := range config.Tunnels {
		result, err := applyOneTunnel(ctx, item, authData, client, kubeconfig, false)
		if err != nil {
			rollbackApplyResults(client, results)
			return results, err
		}
		results = append(results, result)
	}
	if err := ensureDaemonRunning(); err != nil {
		rollbackApplyResults(client, results)
		return results, fmt.Errorf("failed to start local daemon: %w", err)
	}
	for _, result := range results {
		if err := waitForDaemonSession(result.TunnelID, daemonConnectTimeout); err != nil {
			rollbackApplyResults(client, results)
			return results, err
		}
	}
	return results, nil
}

func dashboardActiveKubeClient() (*auth.AuthData, *k8s.Client, string, error) {
	root, err := auth.CurrentSealtunDir()
	if err != nil {
		return nil, nil, "", err
	}
	authData, err := auth.LoadAuthDataFromDir(root)
	if err != nil {
		return nil, nil, "", fmt.Errorf("not logged in. Please run 'sealtun login' first: %w", err)
	}
	kubeconfigPath := filepath.Join(root, "kubeconfig")
	kubeconfig, err := readDashboardRegularFile(kubeconfigPath, "active kubeconfig")
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to read kubeconfig: %w", err)
	}
	client, err := k8s.NewClient(kubeconfigPath, authData)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to init k8s client: %w", err)
	}
	return authData, client, kubeconfig, nil
}

func dashboardActiveScope() (*dashboardScope, error) {
	root, err := auth.CurrentSealtunDir()
	if err != nil {
		return nil, err
	}
	authData, err := auth.LoadAuthDataFromDir(root)
	if err != nil {
		return nil, fmt.Errorf("not logged in. Please run 'sealtun login' first: %w", err)
	}
	kubeconfigPath := filepath.Join(root, "kubeconfig")
	if _, err := readDashboardRegularFile(kubeconfigPath, "active kubeconfig"); err != nil {
		return nil, fmt.Errorf("failed to read kubeconfig: %w", err)
	}
	client, err := k8s.NewClient(kubeconfigPath, authData)
	if err != nil {
		return nil, fmt.Errorf("failed to init k8s client: %w", err)
	}
	return &dashboardScope{region: authData.Region, namespace: client.Namespace()}, nil
}

func readDashboardRegularFile(path, label string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s %s is not a regular file", label, path)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is the active Sealtun kubeconfig under the configured home.
	if err != nil {
		return "", err
	}
	return string(data), nil
}
