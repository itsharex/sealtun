package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestBuildInitPayloadRecommendsDiscoveredTemplate(t *testing.T) {
	previousStatus := initStatusCollector
	previousDiscoverer := initPortDiscoverer
	initStatusCollector = func() (*statusPayload, error) {
		return &statusPayload{
			LoggedIn: true,
			Region:   "https://gzg.sealos.run",
			Kubeconfig: kubeconfigStatus{
				Present:   true,
				Namespace: "ns-demo",
			},
		}, nil
	}
	initPortDiscoverer = fakePortDiscoverer{items: []discoverItem{{Port: 6379, Address: "127.0.0.1", ProcessName: "redis-server"}}}
	t.Cleanup(func() {
		initStatusCollector = previousStatus
		initPortDiscoverer = previousDiscoverer
	})

	payload, err := buildInitPayload(context.Background(), initOptions{Protocol: "auto", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if payload.Recommendation.Template != "redis" || payload.Recommendation.Protocol != "tcp" || payload.Recommendation.LocalPort != 6379 {
		t.Fatalf("unexpected recommendation: %#v", payload.Recommendation)
	}
	if !strings.Contains(payload.Recommendation.Command, "sealtun expose 6379 --protocol tcp") {
		t.Fatalf("unexpected command: %s", payload.Recommendation.Command)
	}
}

func TestInitDefaultDoesNotApplyResources(t *testing.T) {
	previousStatus := initStatusCollector
	previousDiscoverer := initPortDiscoverer
	previousRunner := initApplyRunner
	applied := 0
	initStatusCollector = func() (*statusPayload, error) {
		return &statusPayload{LoggedIn: false}, nil
	}
	initPortDiscoverer = fakePortDiscoverer{}
	initApplyRunner = func(context.Context, *applyFile, bool) ([]applyResult, error) {
		applied++
		return nil, nil
	}
	oldOpts := initOpts
	initOpts = initOptions{JSON: false, Limit: 10}
	t.Cleanup(func() {
		initStatusCollector = previousStatus
		initPortDiscoverer = previousDiscoverer
		initApplyRunner = previousRunner
		initOpts = oldOpts
	})

	cmd := *initCmd
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.RunE(&cmd, nil); err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("default init should not apply resources, applied=%d", applied)
	}
	if !strings.Contains(out.String(), "No resources were created") {
		t.Fatalf("expected non-mutating output, got %s", out.String())
	}
}

func TestInitExplicitProtocolUsesTemplatePortUnlessPortProvided(t *testing.T) {
	rec, err := initRecommendationFromOptions(initOptions{Protocol: "https"}, []discoverItem{{Port: 1082, TemplateHint: "https", ProtocolHint: "https"}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Template != "https" || rec.Protocol != "https" || rec.LocalPort != 1082 {
		t.Fatalf("explicit https should use a discovered web port, got %#v", rec)
	}

	rec, err = initRecommendationFromOptions(initOptions{Protocol: "postgres"}, []discoverItem{{Port: 1082, TemplateHint: "https", ProtocolHint: "https"}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Template != "postgres" || rec.Protocol != "tcp" || rec.LocalPort != 5432 {
		t.Fatalf("explicit postgres should use template default, got %#v", rec)
	}
	rec, err = initRecommendationFromOptions(initOptions{Protocol: "postgres", Port: 15432}, []discoverItem{{Port: 1082, TemplateHint: "https", ProtocolHint: "https"}})
	if err != nil {
		t.Fatal(err)
	}
	if rec.LocalPort != 15432 {
		t.Fatalf("--port should override template default, got %#v", rec)
	}
}

func TestApplyConfigForRecommendationPreservesDomain(t *testing.T) {
	config := applyConfigForRecommendation(initRecommendation{
		Name:      "web",
		Protocol:  "https",
		LocalPort: 3000,
		Domain:    "app.example.com",
		YAML:      "waitDomain: true",
	})
	if len(config.Tunnels) != 1 {
		t.Fatalf("expected one tunnel, got %#v", config)
	}
	tunnel := config.Tunnels[0]
	if tunnel.Domain != "app.example.com" || !tunnel.WaitDomain {
		t.Fatalf("expected domain and waitDomain to be preserved, got %#v", tunnel)
	}
}
