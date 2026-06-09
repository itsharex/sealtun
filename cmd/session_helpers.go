package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labring/sealtun/pkg/auth"
	daemonstate "github.com/labring/sealtun/pkg/daemon"
	"github.com/labring/sealtun/pkg/k8s"
	tunnelprotocol "github.com/labring/sealtun/pkg/protocol"
	"github.com/labring/sealtun/pkg/session"
)

var errMissingSessionKubeconfig = errors.New("session has no embedded kubeconfig")

type sessionSnapshot struct {
	Status             string
	ProcessAlive       bool
	LocalPortReachable bool
}

func findSession(tunnelID string) (*session.TunnelSession, error) {
	sess, err := session.Get(tunnelID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("tunnel session %q not found", tunnelID)
		}
		return nil, fmt.Errorf("load tunnel session %q: %w", tunnelID, err)
	}
	return sess, nil
}

func localPortReachable(port string) bool {
	if port == "" || port == "-" {
		return false
	}

	target := (&url.URL{Scheme: "http", Host: net.JoinHostPort("localhost", port)}).Host
	conn, err := net.DialTimeout("tcp", target, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func k8sClientForSession(sess session.TunnelSession) (*k8s.Client, error) {
	if sess.Namespace == "" {
		return nil, fmt.Errorf("session namespace is missing for tunnel %q", sess.TunnelID)
	}

	if sess.Kubeconfig != "" {
		return k8s.NewClientFromKubeconfig(sess.Kubeconfig, &auth.AuthData{Region: sess.Region})
	}

	authData, err := auth.LoadAuthData()
	if err != nil {
		return nil, fmt.Errorf("%w for tunnel %q and current login is unavailable: %w", errMissingSessionKubeconfig, sess.TunnelID, err)
	}
	if sess.Region == "" {
		return nil, fmt.Errorf("%w for tunnel %q and the legacy session does not record its region", errMissingSessionKubeconfig, sess.TunnelID)
	}
	if authData.Region == "" || sess.Region != authData.Region {
		return nil, fmt.Errorf("%w for tunnel %q; session region is %s but current login region is %s", errMissingSessionKubeconfig, sess.TunnelID, sess.Region, authData.Region)
	}
	if sess.Namespace == "" {
		return nil, fmt.Errorf("%w for tunnel %q and the legacy session does not record its namespace", errMissingSessionKubeconfig, sess.TunnelID)
	}

	root, err := auth.GetSealosDir()
	if err != nil {
		return nil, err
	}
	kubeconfigPath := filepath.Join(root, "kubeconfig")
	if _, err := os.Stat(kubeconfigPath); err != nil {
		return nil, fmt.Errorf("%w for tunnel %q and current kubeconfig is unavailable: %w", errMissingSessionKubeconfig, sess.TunnelID, err)
	}

	client, err := k8s.NewClient(kubeconfigPath, authData)
	if err != nil {
		return nil, err
	}
	if client.Namespace() != sess.Namespace {
		return nil, fmt.Errorf("%w for tunnel %q; session namespace is %s but current kubeconfig namespace is %s", errMissingSessionKubeconfig, sess.TunnelID, sess.Namespace, client.Namespace())
	}
	return client, nil
}

var cleanupSessionResources = func(ctx context.Context, sess session.TunnelSession) error {
	client, err := k8sClientForSession(sess)
	if err != nil {
		return err
	}

	return client.WithNamespace(sess.Namespace).CleanupTunnel(ctx, sess.TunnelID)
}

func pauseSessionResources(ctx context.Context, sess session.TunnelSession) error {
	client, err := k8sClientForSession(sess)
	if err != nil {
		return err
	}

	return client.WithNamespace(sess.Namespace).PauseTunnel(ctx, sess.TunnelID)
}

func resumeSessionResources(ctx context.Context, sess session.TunnelSession) error {
	client, err := k8sClientForSession(sess)
	if err != nil {
		return err
	}

	return client.WithNamespace(sess.Namespace).ResumeTunnel(ctx, sess.TunnelID)
}

func sessionControlHost(sess session.TunnelSession) string {
	if sess.SealosHost != "" {
		return sess.SealosHost
	}
	return sess.Host
}

func sessionProtocol(sess session.TunnelSession) string {
	protocol := tunnelprotocol.Normalize(sess.Protocol)
	if protocol == "" {
		return tunnelprotocol.HTTPS
	}
	return protocol
}

func sessionUsesHTTP(sess session.TunnelSession) bool {
	return sessionProtocol(sess) == tunnelprotocol.HTTPS
}

func normalizePublicHostname(value string) (string, error) {
	host, err := validateCustomDomain(value)
	if err != nil {
		return "", err
	}
	if host == "" {
		return "", fmt.Errorf("public host is missing")
	}
	return host, nil
}

func sessionSealosHostForDomain(sess session.TunnelSession, computed string) string {
	if sess.SealosHost != "" {
		return sess.SealosHost
	}
	if sess.CustomDomain == "" && sess.Host != "" {
		return sess.Host
	}
	return computed
}

func sessionOwnerAlive(sess session.TunnelSession) bool {
	if sess.Mode == "daemon" {
		return daemonstate.Alive()
	}
	return session.OwnerAlive(sess)
}

func classifySession(sess session.TunnelSession, checkLocalPort bool) sessionSnapshot {
	processAlive := sessionOwnerAlive(sess)
	status := session.RuntimeStatusWithOwner(sess, processAlive)
	localReachable := false
	if checkLocalPort {
		localReachable = localPortReachable(sess.LocalPort)
		if status == "active" && processAlive && !localReachable {
			status = "degraded"
		}
	} else if status == "active" && processAlive && sess.Mode != "daemon" {
		status = "running"
	}

	return sessionSnapshot{
		Status:             status,
		ProcessAlive:       processAlive,
		LocalPortReachable: localReachable,
	}
}

func sessionIsStale(sess session.TunnelSession, gracePeriod time.Duration) bool {
	if sessionExpired(sess, time.Now()) {
		return true
	}
	return session.IsStaleWithOwner(sess, gracePeriod, sessionOwnerAlive(sess))
}

func sessionCleanupEligible(sess session.TunnelSession, gracePeriod time.Duration) bool {
	if sessionIsStale(sess, gracePeriod) {
		return true
	}
	return sess.ConnectionState == session.ConnectionStateError
}

func sessionNeedsAutomaticRecovery(sess session.TunnelSession, gracePeriod time.Duration) bool {
	if sessionExpired(sess, time.Now()) {
		return true
	}
	if sess.ConnectionState == session.ConnectionStateStopped {
		return false
	}
	return session.IsStaleWithOwner(sess, gracePeriod, sessionOwnerAlive(sess))
}

func shouldPreserveStoppedSession(sess *session.TunnelSession) bool {
	return sess != nil && sess.ConnectionState == session.ConnectionStateStopped
}

func sessionExpired(sess session.TunnelSession, now time.Time) bool {
	if strings.TrimSpace(sess.ExpiresAt) == "" {
		return false
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(sess.ExpiresAt))
	if err != nil {
		return true
	}
	return !now.Before(expiresAt)
}

func ensureSessionPublicPort(ctx context.Context, sess *session.TunnelSession) {
	if sess == nil || (sess.Protocol != "ssh" && sess.Protocol != "tcp") || sess.PublicPort != 0 {
		return
	}
	client, err := k8sClientForSession(*sess)
	if err != nil {
		return
	}
	port, err := client.WithNamespace(sess.Namespace).TunnelPublicPort(ctx, sess.TunnelID)
	if err != nil || port == 0 {
		return
	}
	sess.PublicPort = port
	_ = session.Update(*sess)
}
