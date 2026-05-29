package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type fakePortDiscoverer struct {
	items []discoverItem
	err   error
}

func (f fakePortDiscoverer) ListListeningPorts(context.Context) ([]discoverItem, error) {
	return append([]discoverItem(nil), f.items...), f.err
}

func TestDiscoverLocalPortsAppliesProtocolTemplateHints(t *testing.T) {
	items, err := discoverLocalPorts(context.Background(), discoverOptions{Limit: 20, Protocol: "auto"}, fakePortDiscoverer{items: []discoverItem{
		{Port: 22, Address: "127.0.0.1"},
		{Port: 3306, Address: "127.0.0.1"},
		{Port: 5432, Address: "127.0.0.1"},
		{Port: 6379, Address: "127.0.0.1"},
		{Port: 1883, Address: "127.0.0.1"},
		{Port: 3000, Address: "127.0.0.1"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]discoverItem{}
	for _, item := range items {
		got[item.Port] = item
	}
	tests := map[int]struct {
		protocol string
		template string
	}{
		22:   {"ssh", "ssh"},
		3306: {"tcp", "mysql"},
		5432: {"tcp", "postgres"},
		6379: {"tcp", "redis"},
		1883: {"tcp", "mqtt"},
		3000: {"https", "https"},
	}
	for port, want := range tests {
		item, ok := got[port]
		if !ok {
			t.Fatalf("missing discovered port %d in %#v", port, got)
		}
		if item.ProtocolHint != want.protocol || item.TemplateHint != want.template || item.Command == "" {
			t.Fatalf("unexpected hint for %d: %#v", port, item)
		}
	}
}

func TestDiscoverLocalPortsFiltersAndLimits(t *testing.T) {
	items, err := discoverLocalPorts(context.Background(), discoverOptions{Limit: 1, Protocol: "tcp"}, fakePortDiscoverer{items: []discoverItem{
		{Port: 3000},
		{Port: 3306},
		{Port: 6379},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ProtocolHint != "tcp" {
		t.Fatalf("expected one tcp item, got %#v", items)
	}
}

func TestDiscoverLimitValidation(t *testing.T) {
	if _, err := parseDiscoverLimit("201", 30); err == nil {
		t.Fatal("expected limit > 200 to be rejected")
	}
	if got, err := parseDiscoverLimit("", 30); err != nil || got != 30 {
		t.Fatalf("expected fallback limit, got %d err=%v", got, err)
	}
}

func TestDiscoverLocalPortsDegradesWhenSystemScanFails(t *testing.T) {
	items, err := discoverLocalPorts(context.Background(), discoverOptions{Limit: 10, Protocol: "auto"}, fakePortDiscoverer{err: errors.New("lsof unavailable")})
	if err != nil {
		t.Fatalf("expected graceful empty result, got %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no items on scan failure, got %#v", items)
	}
}

func TestDiscoverCommandJSONOutputIsStable(t *testing.T) {
	var out bytes.Buffer
	items, err := discoverLocalPorts(context.Background(), discoverOptions{JSON: true, Protocol: "auto", Limit: 10}, fakePortDiscoverer{items: []discoverItem{{Port: 22, Address: "127.0.0.1"}}})
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(&out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(items); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, `"port": 22`) || !strings.Contains(text, `"protocolHint": "ssh"`) {
		t.Fatalf("unexpected json output: %s", text)
	}
}

func TestParseLsofListeningPorts(t *testing.T) {
	out := `COMMAND   PID USER   FD   TYPE DEVICE SIZE/OFF NODE NAME
node     1234 user   20u  IPv4  0x01      0t0  TCP 127.0.0.1:3000 (LISTEN)
sshd       22 root    3u  IPv6  0x02      0t0  TCP *:22 (LISTEN)
`
	items := parseLsofListeningPorts(out)
	if len(items) != 2 {
		t.Fatalf("expected 2 lsof items, got %#v", items)
	}
	if items[0].Port != 3000 || items[0].ProcessName != "node" || items[0].PID != 1234 {
		t.Fatalf("unexpected first item: %#v", items[0])
	}
}
