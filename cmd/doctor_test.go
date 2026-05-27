package cmd

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	daemonstate "github.com/labring/sealtun/pkg/daemon"
	"github.com/labring/sealtun/pkg/k8s"
	"github.com/labring/sealtun/pkg/session"
)

func TestCollectDoctorPayload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := session.Save(session.TunnelSession{
		TunnelID:  "abc123",
		Host:      "abc.example.com",
		LocalPort: "3000",
		PID:       0,
		Namespace: "ns-demo",
		Protocol:  "https",
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	payload, err := collectDoctorPayload()
	if err != nil {
		t.Fatalf("collectDoctorPayload: %v", err)
	}

	if payload.TotalSessions != 1 {
		t.Fatalf("expected 1 total session, got %d", payload.TotalSessions)
	}
	if payload.StaleSessions != 1 {
		t.Fatalf("expected 1 stale session, got %d", payload.StaleSessions)
	}
	if len(payload.Warnings) == 0 {
		t.Fatal("expected warnings to be present")
	}
}

func TestCollectTunnelDoctorPayloadForStoppedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := session.Save(session.TunnelSession{
		TunnelID:        "stopdoc",
		Host:            "stop.example.com",
		LocalPort:       "3000",
		Protocol:        "https",
		Mode:            "daemon",
		ConnectionState: session.ConnectionStateStopped,
		CreatedAt:       time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	payload, err := collectTunnelDoctorPayload(context.Background(), "stopdoc")
	if err != nil {
		t.Fatalf("collectTunnelDoctorPayload returned error: %v", err)
	}
	if payload.Status != "stopped" {
		t.Fatalf("expected stopped status, got %s", payload.Status)
	}
	if len(payload.Checks) < 3 || payload.Checks[1].Status != "skip" || payload.Checks[2].Status != "skip" {
		t.Fatalf("expected stopped owner/local-port checks to be skipped, got %#v", payload.Checks)
	}
	if len(payload.Suggestions) == 0 || !strings.Contains(payload.Suggestions[0], "sealtun start stopdoc") {
		t.Fatalf("expected start suggestion, got %#v", payload.Suggestions)
	}
}

func TestRemoteDoctorChecksSkipScaledToZeroDeployment(t *testing.T) {
	checks := remoteDoctorChecks(&k8s.TunnelDiagnostics{
		Deployment: k8s.DeploymentDiagnostics{
			Exists:          true,
			DesiredReplicas: 0,
			ReadyReplicas:   0,
		},
		Service: k8s.ServiceDiagnostics{Exists: true},
		Ingress: k8s.IngressDiagnostics{Exists: true},
	})
	if len(checks) == 0 || checks[0].Name != "deployment" || checks[0].Status != "skip" {
		t.Fatalf("expected scaled-to-zero deployment check to be skipped, got %#v", checks)
	}
	if len(checks) < 2 || checks[1].Detail != "no ports reported" {
		t.Fatalf("expected empty service ports to have a readable detail, got %#v", checks)
	}
}

func TestRemoteDoctorChecksHideCertificateSecretName(t *testing.T) {
	checks := remoteDoctorChecks(&k8s.TunnelDiagnostics{
		Deployment: k8s.DeploymentDiagnostics{
			Exists:          true,
			DesiredReplicas: 1,
			ReadyReplicas:   1,
		},
		Service: k8s.ServiceDiagnostics{Exists: true, Ports: []string{"http:80"}},
		Ingress: k8s.IngressDiagnostics{Exists: true, Hosts: []string{"app.example.com"}},
		Certificate: &k8s.CertificateDiagnostics{
			Exists:     true,
			Ready:      true,
			SecretName: "sealtun-webdev-custom-tls",
		},
	})

	for _, check := range checks {
		if strings.Contains(check.Detail, "sealtun-webdev-custom-tls") {
			t.Fatalf("doctor check should not expose certificate secret names, got %#v", checks)
		}
	}
}

func TestCertificateDoctorDetail(t *testing.T) {
	tests := []struct {
		name string
		cert *k8s.CertificateDiagnostics
		want string
	}{
		{name: "nil", cert: nil, want: "missing"},
		{name: "missing", cert: &k8s.CertificateDiagnostics{}, want: "missing"},
		{name: "not ready", cert: &k8s.CertificateDiagnostics{Exists: true}, want: "not ready"},
		{name: "ready", cert: &k8s.CertificateDiagnostics{Exists: true, Ready: true}, want: "ready"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := certificateDoctorDetail(tt.cert); got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestCollectDoctorPayloadDoesNotRequireDaemonForForegroundSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := session.Save(session.TunnelSession{
		TunnelID:  "fg123",
		Host:      "fg.example.com",
		LocalPort: "3000",
		PID:       currentPIDForTest(),
		Mode:      "foreground",
		Namespace: "ns-demo",
		Protocol:  "https",
		CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	payload, err := collectDoctorPayload()
	if err != nil {
		t.Fatalf("collectDoctorPayload: %v", err)
	}

	for _, warning := range payload.Warnings {
		if warning == "daemon is not running; daemon-managed tunnels will not reconnect until it starts" {
			t.Fatal("foreground-only sessions should not require daemon")
		}
	}
}

func TestCollectDoctorPayloadCountsConnectingDaemonSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := daemonstate.SaveState(os.Getpid()); err != nil {
		t.Fatalf("SaveState returned error: %v", err)
	}
	t.Cleanup(func() {
		_ = daemonstate.DeleteState()
	})

	if err := session.Save(session.TunnelSession{
		TunnelID:        "daemon123",
		Host:            "daemon.example.com",
		LocalPort:       "3000",
		PID:             os.Getpid(),
		Mode:            "daemon",
		Namespace:       "ns-demo",
		Protocol:        "https",
		ConnectionState: session.ConnectionStateConnecting,
		CreatedAt:       time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	payload, err := collectDoctorPayload()
	if err != nil {
		t.Fatalf("collectDoctorPayload: %v", err)
	}
	if payload.ConnectingSessions != 1 {
		t.Fatalf("expected 1 connecting session, got %d", payload.ConnectingSessions)
	}
	if payload.StaleSessions != 0 {
		t.Fatalf("expected no stale sessions, got %d", payload.StaleSessions)
	}
}

func TestSessionIsStaleTreatsStoppedDaemonSessionAsCleanupEligible(t *testing.T) {
	if !sessionIsStale(session.TunnelSession{
		Mode:            "daemon",
		ConnectionState: session.ConnectionStateStopped,
		UpdatedAt:       time.Now().Format(time.RFC3339),
	}, time.Minute) {
		t.Fatal("expected stopped daemon session to be cleanup eligible")
	}
}

func TestSessionNeedsAutomaticRecoverySkipsStoppedSession(t *testing.T) {
	if sessionNeedsAutomaticRecovery(session.TunnelSession{
		Mode:            "daemon",
		ConnectionState: session.ConnectionStateStopped,
		UpdatedAt:       time.Now().Add(-time.Hour).Format(time.RFC3339),
	}, time.Minute) {
		t.Fatal("expected stopped session to be preserved during automatic recovery")
	}
}

func TestSessionNeedsAutomaticRecoveryIncludesExpiredStoppedSession(t *testing.T) {
	if !sessionNeedsAutomaticRecovery(session.TunnelSession{
		Mode:            "daemon",
		ConnectionState: session.ConnectionStateStopped,
		ExpiresAt:       time.Now().Add(-time.Hour).Format(time.RFC3339),
		UpdatedAt:       time.Now().Format(time.RFC3339),
	}, time.Minute) {
		t.Fatal("expected expired stopped session to be automatic cleanup eligible")
	}
}

func TestTunnelCleanupShouldPreserveStoppedSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := session.Save(session.TunnelSession{
		TunnelID:        "paused123",
		ConnectionState: session.ConnectionStateStopped,
		CreatedAt:       time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	if !tunnelCleanupShouldPreserve("paused123") {
		t.Fatal("expected stopped session to preserve remote resources during foreground cleanup")
	}
	if tunnelCleanupShouldPreserve("missing") {
		t.Fatal("expected missing session to allow cleanup")
	}
}

func TestStartRejectsExpiredSessionBeforeRemoteMutation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := session.Save(session.TunnelSession{
		TunnelID:        "expired123",
		Secret:          "secret",
		ExpiresAt:       time.Now().Add(-time.Hour).Format(time.RFC3339),
		ConnectionState: session.ConnectionStateStopped,
		CreatedAt:       time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	err := startCmd.RunE(startCmd, []string{"expired123"})
	if err == nil || !strings.Contains(err.Error(), "has expired") {
		t.Fatalf("expected expired start rejection, got %v", err)
	}
}

func TestRollbackStartedTunnelSessionMarksSessionStopped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sess := session.TunnelSession{
		TunnelID:        "rollbackstart",
		Region:          "https://gzg.sealos.run",
		Namespace:       "ns-demo",
		Kubeconfig:      "kubeconfig",
		Protocol:        "https",
		Host:            "sealtun-rollbackstart-ns-demo.sealosgzg.site",
		LocalPort:       "3000",
		Secret:          "secret",
		Mode:            "daemon",
		ConnectionState: session.ConnectionStatePending,
	}
	if err := session.Save(sess); err != nil {
		t.Fatal(err)
	}

	err := rollbackStartedTunnelSession(sess, fmt.Errorf("daemon failed"))
	if err == nil || !strings.Contains(err.Error(), "daemon failed") {
		t.Fatalf("expected original error, got %v", err)
	}
	got, err := session.Get(sess.TunnelID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConnectionState != session.ConnectionStateStopped {
		t.Fatalf("expected stopped state, got %q", got.ConnectionState)
	}
	if got.LastError != "daemon failed" {
		t.Fatalf("expected last error to preserve cause, got %q", got.LastError)
	}
}

func TestCollectDoctorPayloadCountsStoppedSeparately(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := session.Save(session.TunnelSession{
		TunnelID:        "stopped123",
		Host:            "stopped.example.com",
		LocalPort:       "3000",
		Mode:            "daemon",
		ConnectionState: session.ConnectionStateStopped,
		CreatedAt:       time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save session: %v", err)
	}

	payload, err := collectDoctorPayload()
	if err != nil {
		t.Fatalf("collectDoctorPayload: %v", err)
	}
	if payload.StoppedSessions != 1 {
		t.Fatalf("expected 1 stopped session, got %d", payload.StoppedSessions)
	}
	if payload.StaleSessions != 0 {
		t.Fatalf("expected no stale sessions, got %d", payload.StaleSessions)
	}
}

func TestCollectDoctorPayloadCountsDegradedSessionsSeparately(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	activePort := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)

	if err := session.Save(session.TunnelSession{
		TunnelID:        "active-down",
		Host:            "active.example.com",
		LocalPort:       "65534",
		PID:             currentPIDForTest(),
		Mode:            "foreground",
		Namespace:       "ns-demo",
		Protocol:        "https",
		ConnectionState: session.ConnectionStateConnected,
		CreatedAt:       time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save active session: %v", err)
	}

	if err := session.Save(session.TunnelSession{
		TunnelID:        "connecting-up",
		Host:            "connecting.example.com",
		LocalPort:       activePort,
		PID:             currentPIDForTest(),
		Mode:            "daemon",
		Namespace:       "ns-demo",
		Protocol:        "https",
		ConnectionState: session.ConnectionStateConnecting,
		CreatedAt:       time.Now().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save connecting session: %v", err)
	}

	payload, err := collectDoctorPayload()
	if err != nil {
		t.Fatalf("collectDoctorPayload: %v", err)
	}
	if payload.ActiveSessions != 0 {
		t.Fatalf("expected no active sessions, got %d", payload.ActiveSessions)
	}
	if payload.DegradedSessions != 1 {
		t.Fatalf("expected 1 degraded session, got %d", payload.DegradedSessions)
	}
	if payload.ReachableActivePorts != 0 {
		t.Fatalf("expected no reachable active ports, got %d", payload.ReachableActivePorts)
	}
	if !containsWarning(payload.Warnings, "1 tunnel session(s) have a live owner but unreachable local port") {
		t.Fatalf("expected degraded warning, got %#v", payload.Warnings)
	}
}

func containsWarning(warnings []string, want string) bool {
	for _, warning := range warnings {
		if warning == want {
			return true
		}
	}
	return false
}
