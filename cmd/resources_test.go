package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/labring/sealtun/pkg/auth"
	"github.com/labring/sealtun/pkg/k8s"
	"github.com/labring/sealtun/pkg/session"
)

func TestPrintTunnelResourcesHidesSecretDataAndShowsHints(t *testing.T) {
	payload := &k8s.TunnelResourceList{
		Namespace: "ns-demo",
		TunnelID:  "abc123",
		Resources: []k8s.TunnelResource{{
			Kind:      "Secret",
			Name:      "sealtun-abc123-auth",
			Status:    "Opaque",
			Namespace: "ns-demo",
			Managed:   true,
			CostHints: []string{"secret data hidden"},
		}},
	}
	cmd := *resourcesCmd
	var out bytes.Buffer
	cmd.SetOut(&out)
	printTunnelResources(&cmd, payload)
	text := out.String()
	if !strings.Contains(text, "resource hints show Kubernetes occupancy") || !strings.Contains(text, "secret data hidden") {
		t.Fatalf("expected resources note and hint, got %s", text)
	}
	if strings.Contains(text, "Data:") || strings.Contains(text, "token") || strings.Contains(text, "tls.key") {
		t.Fatalf("resources output should not expose secret data fields: %s", text)
	}
}

func TestActiveScopedSessionRejectsOutsideScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := auth.SaveAuthData(auth.AuthData{Region: "https://gzg.sealos.run"}, dashboardTestKubeconfig(t, "ns-a")); err != nil {
		t.Fatal(err)
	}
	if err := session.Save(session.TunnelSession{
		TunnelID:  "otherns",
		Region:    "https://gzg.sealos.run",
		Namespace: "ns-b",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := activeScopedSession("otherns"); err == nil || !strings.Contains(err.Error(), "outside the active scope") {
		t.Fatalf("expected active scope rejection, got %v", err)
	}
}
