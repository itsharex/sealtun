package cmd

import (
	"strings"
	"testing"
)

func TestBuildProtocolTemplateForPostgres(t *testing.T) {
	tmpl, err := buildProtocolTemplate("postgres", "", 0, "")
	if err != nil {
		t.Fatalf("buildProtocolTemplate returned error: %v", err)
	}
	if tmpl.Protocol != "tcp" || tmpl.Name != "postgres" || tmpl.LocalPort != 5432 {
		t.Fatalf("unexpected postgres template: %#v", tmpl)
	}
	if !strings.Contains(tmpl.Command, "sealtun expose 5432 --protocol tcp") {
		t.Fatalf("unexpected command: %s", tmpl.Command)
	}
	if !strings.Contains(tmpl.Config, "protocol: tcp") {
		t.Fatalf("expected tcp YAML, got:\n%s", tmpl.Config)
	}
}

func TestBuildProtocolTemplateForHTTPSDomain(t *testing.T) {
	tmpl, err := buildProtocolTemplate("https", "api", 8080, "API.Example.COM.")
	if err != nil {
		t.Fatalf("buildProtocolTemplate returned error: %v", err)
	}
	if !strings.Contains(tmpl.Command, "--domain api.example.com --wait-domain") {
		t.Fatalf("expected normalized domain in command, got %s", tmpl.Command)
	}
	if !strings.Contains(tmpl.Config, "domain: api.example.com") || !strings.Contains(tmpl.Config, "waitDomain: true") {
		t.Fatalf("expected domain YAML, got:\n%s", tmpl.Config)
	}
}

func TestBuildProtocolTemplateRejectsDomainForTCP(t *testing.T) {
	_, err := buildProtocolTemplate("redis", "", 0, "redis.example.com")
	if err == nil || !strings.Contains(err.Error(), "only supported for https") {
		t.Fatalf("expected tcp domain rejection, got %v", err)
	}
}

func TestBuildProtocolTemplateRejectsUnknownKind(t *testing.T) {
	_, err := buildProtocolTemplate("udp", "", 0, "")
	if err == nil || !strings.Contains(err.Error(), "unsupported template") {
		t.Fatalf("expected unsupported template error, got %v", err)
	}
}
