package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/labring/sealtun/pkg/session"
)

func TestRotateServerSecretRejectsStoppedAndExpiredBeforeRemoteMutation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	stopped := session.TunnelSession{
		TunnelID:        "stopped",
		Host:            "stopped.example.com",
		LocalPort:       "3000",
		Secret:          "secret",
		ConnectionState: session.ConnectionStateStopped,
	}
	if err := session.Save(stopped); err != nil {
		t.Fatal(err)
	}
	if _, err := rotateTunnelServerSecret(context.Background(), "stopped"); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("expected stopped tunnel rejection, got %v", err)
	}

	expired := session.TunnelSession{
		TunnelID:  "expired",
		Protocol:  "https",
		Host:      "expired.example.com",
		LocalPort: "3000",
		Secret:    "secret",
		ExpiresAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	}
	if err := session.Save(expired); err != nil {
		t.Fatal(err)
	}
	if _, err := rotateTunnelServerSecret(context.Background(), "expired"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired tunnel rejection, got %v", err)
	}
}
