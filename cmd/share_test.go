package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/labring/sealtun/pkg/accesspolicy"
	"github.com/labring/sealtun/pkg/session"
)

func TestGenerateShareTokenIsValidAccessPolicyToken(t *testing.T) {
	t.Parallel()

	token, err := generateShareToken()
	if err != nil {
		t.Fatalf("generateShareToken returned error: %v", err)
	}
	if len(token) < 8 {
		t.Fatalf("expected generated token to be at least 8 characters, got %q", token)
	}
	if _, err := accesspolicy.HashToken(token); err != nil {
		t.Fatalf("generated token should be hashable: %v", err)
	}
}

func TestReplaceTemporaryTokenReplacesByName(t *testing.T) {
	t.Parallel()

	tokens := []session.TemporaryToken{
		{Name: "review", TokenHash: "old", ExpiresAt: "2026-01-01T00:00:00Z"},
	}
	got := replaceTemporaryToken(tokens, session.TemporaryToken{Name: "review", TokenHash: "new", ExpiresAt: "2026-01-02T00:00:00Z"})
	if len(got) != 1 {
		t.Fatalf("expected one token, got %#v", got)
	}
	if got[0].TokenHash != "new" {
		t.Fatalf("expected token to be replaced, got %#v", got[0])
	}
}

func TestListShareLinksMarksExpired(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sess := session.TunnelSession{
		TunnelID:  "web",
		Protocol:  "https",
		Host:      "web.example.com",
		LocalPort: "3000",
		AccessPolicy: &session.AccessPolicy{TemporaryTokens: []session.TemporaryToken{
			{Name: "old", TokenHash: strings.Repeat("a", 71), ExpiresAt: "2026-01-01T00:00:00Z"},
			{Name: "new", TokenHash: strings.Repeat("b", 71), ExpiresAt: "2026-01-01T02:00:00Z"},
		}},
	}
	if err := session.Save(sess); err != nil {
		t.Fatal(err)
	}

	items, err := listShareLinks("web", time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("listShareLinks returned error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected two links, got %#v", items)
	}
	if !items[0].Expired || items[1].Expired {
		t.Fatalf("unexpected expiration status: %#v", items)
	}
}

func TestCreateShareLinkRejectsStoppedAndExpiredSessionsBeforeRemoteMutation(t *testing.T) {
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
	if _, err := createShareLink(context.Background(), "stopped", "review", time.Hour, "review-token"); err == nil || !strings.Contains(err.Error(), "stopped") {
		t.Fatalf("expected stopped tunnel rejection, got %v", err)
	}

	expired := session.TunnelSession{
		TunnelID:  "expired",
		Protocol:  "https",
		Host:      "expired.example.com",
		LocalPort: "3000",
		Secret:    "secret",
		ExpiresAt: "2026-01-01T00:00:00Z",
	}
	if err := session.Save(expired); err != nil {
		t.Fatal(err)
	}
	if _, err := createShareLink(context.Background(), "expired", "review", time.Hour, "review-token"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired tunnel rejection, got %v", err)
	}
}

func TestSharePublicHostFallsBackToSealosHost(t *testing.T) {
	t.Parallel()

	got := sharePublicHost(session.TunnelSession{SealosHost: "sealtun-web.example.com"})
	if got != "sealtun-web.example.com" {
		t.Fatalf("expected Sealos host fallback, got %q", got)
	}
}

func TestCloneAccessPolicyPreservesRateLimitAndAudit(t *testing.T) {
	t.Parallel()

	got := cloneAccessPolicy(&session.AccessPolicy{
		RateLimit: "60/m",
		Audit:     &session.AuditConfig{Enabled: true},
	})
	if got.RateLimit != "60/m" || got.Audit == nil || !got.Audit.Enabled {
		t.Fatalf("expected rate limit and audit to be cloned, got %#v", got)
	}
	got.Audit.Enabled = false
	original := &session.AccessPolicy{Audit: &session.AuditConfig{Enabled: true}}
	cloned := cloneAccessPolicy(original)
	cloned.Audit.Enabled = false
	if !original.Audit.Enabled {
		t.Fatal("clone must not alias audit config")
	}
}

func TestRotateShareLinkRequiresExistingNameBeforeRemoteMutation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	hash, err := accesspolicy.HashToken("old-token")
	if err != nil {
		t.Fatal(err)
	}
	sess := session.TunnelSession{
		TunnelID:  "web",
		Protocol:  "https",
		Host:      "web.example.com",
		LocalPort: "3000",
		Secret:    "secret",
		AccessPolicy: &session.AccessPolicy{
			RateLimit: "60/m",
			Audit:     &session.AuditConfig{Enabled: true},
			TemporaryTokens: []session.TemporaryToken{{
				Name:      "review",
				TokenHash: hash,
				TTL:       "1h",
				ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			}},
		},
	}
	if err := session.Save(sess); err != nil {
		t.Fatal(err)
	}
	if _, err := rotateShareLink(context.Background(), "web", "missing", time.Hour); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected missing link rejection, got %v", err)
	}
}
