package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labring/sealtun/pkg/auth"
	"github.com/labring/sealtun/pkg/session"
)

func TestValidateDashboardListenRejectsNonLoopbackByDefault(t *testing.T) {
	t.Parallel()

	if _, err := validateDashboardListen("0.0.0.0", false); err == nil {
		t.Fatal("expected non-loopback dashboard listen address to be rejected")
	}
	if _, err := validateDashboardListen("", false); err == nil {
		t.Fatal("expected empty dashboard listen address to be rejected because it binds all interfaces")
	}
}

func TestValidateDashboardListenAllowsLoopbackAndExplicitRemote(t *testing.T) {
	t.Parallel()

	loopback, err := validateDashboardListen("127.0.0.1", false)
	if err != nil {
		t.Fatalf("loopback address should be allowed: %v", err)
	}
	if !loopback {
		t.Fatal("expected 127.0.0.1 to be classified as loopback")
	}

	loopback, err = validateDashboardListen("0.0.0.0", true)
	if err != nil {
		t.Fatalf("explicit remote dashboard listen should be allowed: %v", err)
	}
	if loopback {
		t.Fatal("expected 0.0.0.0 to remain classified as non-loopback")
	}
}

func TestDashboardStaticIconRequiresGET(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/favicon.svg", nil)
	rec := httptest.NewRecorder()
	serveDashboardFavicon(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("expected Allow GET header, got %q", got)
	}
}

func TestDashboardSummaryRequiresToken(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	rec := httptest.NewRecorder()
	dashboardServer{token: "secret"}.serveDashboardSummary(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestDashboardAPIRequiresToken(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/api/tunnels/abc123/stop", strings.NewReader(`{"confirm":"stop:abc123"}`))
	rec := httptest.NewRecorder()
	dashboardServer{token: "secret"}.serveAPI(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	var payload dashboardAPIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.OK || payload.Error == "" {
		t.Fatalf("expected JSON error payload, got %#v", payload)
	}
}

func TestRequireDashboardConfirmRejectsMissingConfirmation(t *testing.T) {
	rec := httptest.NewRecorder()
	if requireDashboardConfirm(rec, "", "stop", "abc123") {
		t.Fatal("expected missing confirmation to be rejected")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "stop:abc123") {
		t.Fatalf("expected required confirmation in response, got %s", rec.Body.String())
	}
}

func TestReadDashboardJSONRejectsOversizedBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/apply", bytes.NewReader([]byte(`{"yaml":"`+strings.Repeat("x", 32)+`"}`)))
	_, err := readDashboardJSON[dashboardApplyRequest](req, 8)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized body error, got %v", err)
	}
}

func TestReadDashboardJSONRejectsTrailingJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/apply", strings.NewReader(`{"yaml":"version: v1"}{"yaml":"second"}`))
	_, err := readDashboardJSON[dashboardApplyRequest](req, 1024)
	if err == nil || !strings.Contains(err.Error(), "single JSON object") {
		t.Fatalf("expected trailing JSON error, got %v", err)
	}
}

func TestDashboardApplyYAMLRejectsOversizedYAML(t *testing.T) {
	body := `{"yaml":"` + strings.Repeat("x", applyFileMaxBytes+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/apply/dry-run", strings.NewReader(body))
	req.Header.Set("X-Sealtun-Dashboard-Token", "secret")
	rec := httptest.NewRecorder()
	dashboardServer{token: "secret"}.serveApplyYAML(rec, req, true, false)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "too large") {
		t.Fatalf("expected size error, got %s", rec.Body.String())
	}
}

func TestDashboardApplyYAMLRequiresConfirmation(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/api/apply", strings.NewReader(`{"yaml":"version: v1\ntunnels:\n  - name: web\n    localPort: 3000\n"}`))
	req.Header.Set("X-Sealtun-Dashboard-Token", "secret")
	rec := httptest.NewRecorder()
	dashboardServer{token: "secret"}.serveApplyYAML(rec, req, false, false)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "apply:dashboard-yaml") {
		t.Fatalf("expected required confirmation in response, got %s", rec.Body.String())
	}
}

func TestDashboardLogQueryValidation(t *testing.T) {
	if _, err := parseDashboardTail("1001"); err == nil {
		t.Fatal("expected tail > 1000 to be rejected")
	}
	if got, err := parseDashboardTail(""); err != nil || got != 200 {
		t.Fatalf("expected default tail 200, got %d err=%v", got, err)
	}
	if _, err := parseDashboardSince("-1s"); err == nil {
		t.Fatal("expected negative since to be rejected")
	}
}

func TestDashboardActiveKubeClientDoesNotMigrateLegacySealosConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyDir := filepath.Join(home, ".sealos")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "auth.json"), []byte(`{"region":"https://legacy.sealos.run"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "kubeconfig"), []byte("legacy-kubeconfig"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := dashboardActiveKubeClient(); err == nil {
		t.Fatal("expected missing active sealtun login error")
	}
	if _, err := os.Stat(filepath.Join(home, ".sealtun", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("dashboard write path must not migrate legacy auth into ~/.sealtun, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".sealtun", "kubeconfig")); !os.IsNotExist(err) {
		t.Fatalf("dashboard write path must not migrate legacy kubeconfig into ~/.sealtun, stat err=%v", err)
	}
}

func TestDashboardHomeDisablesCaching(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	dashboardServer{token: "secret", embedToken: true}.serveHome(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %q", got)
	}
}

func TestDashboardHomeDoesNotEmbedTokenForRemoteMode(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	dashboardServer{token: "secret", embedToken: false}.serveHome(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("remote dashboard home must not expose the API token")
	}
}

func TestDashboardContextDoesNotReadSavedProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	profilesDir := filepath.Join(home, ".sealtun", "profiles", "broken")
	if err := os.MkdirAll(profilesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "auth.json"), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/context", nil)
	req.Header.Set("X-Sealtun-Dashboard-Token", "secret")
	rec := httptest.NewRecorder()
	dashboardServer{token: "secret"}.serveContext(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload dashboardContextPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), `"profiles"`) {
		t.Fatalf("dashboard context must not expose saved profiles: %s", rec.Body.String())
	}
	for _, warning := range payload.Warnings {
		if strings.Contains(warning, "broken") || strings.Contains(warning, "not-json") {
			t.Fatalf("dashboard context read saved profile details: %q", warning)
		}
	}
}

func TestDashboardContextDoesNotExposeKnownRegionsWhenLoggedOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	req := httptest.NewRequest(http.MethodGet, "/api/context", nil)
	req.Header.Set("X-Sealtun-Dashboard-Token", "secret")
	rec := httptest.NewRecorder()
	dashboardServer{token: "secret"}.serveContext(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload dashboardContextPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Regions) != 0 {
		t.Fatalf("dashboard context must not expose built-in regions while logged out: %#v", payload.Regions)
	}
	if strings.Contains(rec.Body.String(), "gzg.sealos.run") || strings.Contains(rec.Body.String(), "hzh.sealos.run") || strings.Contains(rec.Body.String(), "cloud.sealos.io") {
		t.Fatalf("logged-out context leaked known regions: %s", rec.Body.String())
	}
}

func TestDashboardRegionItemsOnlyExposeActiveRegion(t *testing.T) {
	items := dashboardRegionItemsForAuth(&auth.AuthData{
		Region:       "https://gzg.sealos.run",
		SealosDomain: "custom.example",
	})
	if len(items) != 1 {
		t.Fatalf("expected exactly one active region item, got %#v", items)
	}
	if !items[0].Current || items[0].URL != "https://gzg.sealos.run" || items[0].SealosDomain != "custom.example" {
		t.Fatalf("unexpected active region item: %#v", items[0])
	}
}

func TestSanitizeDashboardTunnelHostsHidesInvalidLegacyHosts(t *testing.T) {
	items, warnings := sanitizeDashboardTunnelHosts([]listItem{{
		TunnelID:   "abc123",
		Host:       "public.example.com@127.0.0.1",
		SealosHost: "sealtun-abc123-default.sealosgzg.site",
	}})

	if len(items) != 1 {
		t.Fatalf("expected one item, got %#v", items)
	}
	if items[0].Host != "" {
		t.Fatalf("expected invalid public host to be hidden, got %q", items[0].Host)
	}
	if items[0].SealosHost != "sealtun-abc123-default.sealosgzg.site" {
		t.Fatalf("expected valid Sealos host to remain, got %q", items[0].SealosHost)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "invalid public host") {
		t.Fatalf("expected invalid-host warning, got %#v", warnings)
	}
}

func TestDashboardHomeDoesNotExposeProfileSwitchEndpoint(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	dashboardServer{token: "secret", embedToken: true}.serveHome(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "/api/profile/use") || strings.Contains(rec.Body.String(), "data-profile-use") {
		t.Fatal("dashboard UI must not expose profile switching controls")
	}
}

func TestDashboardScopedSessionsFiltersActiveRegionAndNamespace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, sess := range []session.TunnelSession{
		{TunnelID: "active1", Region: "https://gzg.sealos.run", Namespace: "ns-a"},
		{TunnelID: "otherregion", Region: "https://hzh.sealos.run", Namespace: "ns-a"},
		{TunnelID: "otherns", Region: "https://gzg.sealos.run", Namespace: "ns-b"},
	} {
		if err := session.Save(sess); err != nil {
			t.Fatal(err)
		}
	}

	root, err := auth.CurrentSealtunDir()
	if err != nil {
		t.Fatal(err)
	}
	sessions, warnings, err := dashboardScopedSessions(root, &dashboardScope{region: "https://gzg.sealos.run", namespace: "ns-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].TunnelID != "active1" {
		t.Fatalf("expected only active scoped session, got %#v", sessions)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "skipped 2 session") {
		t.Fatalf("expected skipped-session warning, got %#v", warnings)
	}
}

func TestDashboardScopedSessionRejectsOutsideActiveScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	kubeconfig := dashboardTestKubeconfig(t, "ns-a")
	if err := auth.SaveAuthData(auth.AuthData{Region: "https://gzg.sealos.run"}, kubeconfig); err != nil {
		t.Fatal(err)
	}
	if err := session.Save(session.TunnelSession{TunnelID: "otherns", Region: "https://gzg.sealos.run", Namespace: "ns-b"}); err != nil {
		t.Fatal(err)
	}

	if _, err := dashboardScopedSession("otherns"); err == nil || !strings.Contains(err.Error(), "outside the active dashboard scope") {
		t.Fatalf("expected active scope rejection, got %v", err)
	}
}

func TestDashboardDiffRejectsExistingSessionOutsideActiveScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	kubeconfig := dashboardTestKubeconfig(t, "ns-a")
	if err := auth.SaveAuthData(auth.AuthData{Region: "https://gzg.sealos.run"}, kubeconfig); err != nil {
		t.Fatal(err)
	}
	if err := session.Save(session.TunnelSession{TunnelID: "web", Region: "https://gzg.sealos.run", Namespace: "ns-b"}); err != nil {
		t.Fatal(err)
	}

	_, err := runDashboardDiffContent([]byte("version: v1\ntunnels:\n  - name: web\n    localPort: 3000\n"))
	if err == nil || !strings.Contains(err.Error(), "active dashboard scope") {
		t.Fatalf("expected active scope diff rejection, got %v", err)
	}
}

func dashboardTestKubeconfig(t *testing.T, namespace string) string {
	t.Helper()
	addr := "127.0.0.1:1"
	if listener, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		addr = listener.Addr().String()
		_ = listener.Close()
	}
	return `apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://` + addr + `
    insecure-skip-tls-verify: true
contexts:
- name: test
  context:
    cluster: test
    user: test
    namespace: ` + namespace + `
current-context: test
users:
- name: test
  user:
    token: test
`
}

func TestDashboardScopeUsesActiveKubeconfigOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := auth.SaveAuthData(auth.AuthData{Region: "https://gzg.sealos.run"}, "not yaml"); err != nil {
		t.Fatal(err)
	}

	status, err := collectStatus()
	if err != nil {
		t.Fatal(err)
	}
	root, err := auth.CurrentSealtunDir()
	if err != nil {
		t.Fatal(err)
	}
	scope, warnings := dashboardScopeFromStatus(root, status)
	if scope != nil {
		t.Fatalf("expected invalid active kubeconfig to prevent dashboard scope")
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "active kubeconfig") {
		t.Fatalf("expected active kubeconfig warning, got %#v", warnings)
	}
}

func TestDashboardDoesNotMigrateLegacySealosConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyDir := filepath.Join(home, ".sealos")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "auth.json"), []byte(`{"region":"https://legacy.sealos.run"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "kubeconfig"), []byte("legacy-kubeconfig"), 0o600); err != nil {
		t.Fatal(err)
	}

	payload := collectDashboardPayload(context.Background())

	if payload.Status == nil {
		t.Fatal("expected dashboard status payload")
	}
	if payload.Status.LoggedIn {
		t.Fatalf("dashboard must not treat legacy ~/.sealos auth as active login: %#v", payload.Status)
	}
	if _, err := os.Stat(filepath.Join(home, ".sealtun", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("dashboard must not migrate legacy auth into ~/.sealtun, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".sealtun", "kubeconfig")); !os.IsNotExist(err) {
		t.Fatalf("dashboard must not migrate legacy kubeconfig into ~/.sealtun, stat err=%v", err)
	}
}

func TestDashboardDoesNotCreateConfigDirWhenLoggedOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	payload := collectDashboardPayload(context.Background())

	if payload.Status == nil {
		t.Fatal("expected dashboard status payload")
	}
	if payload.Status.LoggedIn {
		t.Fatalf("dashboard must be logged out with empty HOME: %#v", payload.Status)
	}
	if _, err := os.Stat(filepath.Join(home, ".sealtun")); !os.IsNotExist(err) {
		t.Fatalf("dashboard must not create ~/.sealtun while logged out, stat err=%v", err)
	}
}
