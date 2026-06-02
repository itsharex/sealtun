package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWatchJSONEmitsStableTunnelEvent(t *testing.T) {
	previous := watchTunnelCollector
	watchTunnelCollector = func(context.Context, string) (*tunnelDoctorPayload, error) {
		return &tunnelDoctorPayload{
			TunnelID:           "abc123",
			Status:             "active",
			Protocol:           "https",
			Endpoint:           "https://abc.example.com",
			LocalTarget:        "localhost:3000",
			LocalPortReachable: true,
		}, nil
	}
	t.Cleanup(func() { watchTunnelCollector = previous })

	cmd := *watchCmd
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runWatch(&cmd, "abc123", watchOptions{JSON: true, Interval: time.Millisecond, Count: 1}); err != nil {
		t.Fatal(err)
	}
	var event watchEvent
	if err := json.Unmarshal(out.Bytes(), &event); err != nil {
		t.Fatalf("unmarshal watch event: %v\n%s", err, out.String())
	}
	if event.Type != "tunnel" || event.Tunnel == nil || event.Tunnel.TunnelID != "abc123" {
		t.Fatalf("unexpected watch event: %#v", event)
	}
}

func TestWatchTextSummaryUsesGlobalCollector(t *testing.T) {
	previous := watchGlobalCollector
	watchGlobalCollector = func(context.Context) (*doctorPayload, error) {
		return &doctorPayload{TotalSessions: 2, ActiveSessions: 1, StoppedSessions: 1, DaemonRunning: true}, nil
	}
	t.Cleanup(func() { watchGlobalCollector = previous })

	cmd := *watchCmd
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := runWatch(&cmd, "", watchOptions{Interval: time.Millisecond, Count: 1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sessions=2") || !strings.Contains(out.String(), "active=1") {
		t.Fatalf("unexpected watch output: %s", out.String())
	}
}

func TestWatchReturnsErrorWhenCollectorFails(t *testing.T) {
	previous := watchTunnelCollector
	watchTunnelCollector = func(context.Context, string) (*tunnelDoctorPayload, error) {
		return nil, errors.New("missing tunnel")
	}
	t.Cleanup(func() { watchTunnelCollector = previous })

	cmd := *watchCmd
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := runWatch(&cmd, "missing", watchOptions{JSON: true, Interval: time.Millisecond, Count: 1})
	if err == nil || !strings.Contains(err.Error(), "missing tunnel") {
		t.Fatalf("expected collector error, got %v", err)
	}
	var event watchEvent
	if decodeErr := json.Unmarshal(out.Bytes(), &event); decodeErr != nil {
		t.Fatalf("unmarshal watch event: %v\n%s", decodeErr, out.String())
	}
	if event.Error != "missing tunnel" {
		t.Fatalf("expected error event, got %#v", event)
	}
}
