package tunnel

import (
	"net"
	"strings"
	"testing"
	"time"

	tunnelprotocol "github.com/labring/sealtun/pkg/protocol"
)

func TestUnavailableResponse(t *testing.T) {
	t.Parallel()

	response := unavailableResponse("3000")

	if !strings.HasPrefix(response, "HTTP/1.1 502 Bad Gateway\r\n") {
		t.Fatalf("unexpected status line: %q", response)
	}
	if !strings.Contains(response, "Content-Type: text/html; charset=utf-8\r\n") {
		t.Fatal("missing content type header")
	}
	if !strings.Contains(response, "Sealtun Tunnel Status") {
		t.Fatal("missing sealtun status shell")
	}
	if !strings.Contains(response, "localhost:3000") {
		t.Fatal("response should show expected local target")
	}
	if !strings.Contains(response, "Refresh this page after the local service is ready.") {
		t.Fatal("response should explain recovery step")
	}
	if !strings.Contains(response, "<strong>3000</strong>") {
		t.Fatal("response should mention the local port")
	}
}

func TestRawTCPLocalForwardingDoesNotWriteHTTPFallback(t *testing.T) {
	t.Parallel()

	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		handleLocalForwarding(server, "1", tunnelprotocol.TCP)
		close(done)
	}()

	buffer := make([]byte, 1)
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	n, err := client.Read(buffer)
	_ = client.Close()
	<-done

	if n != 0 {
		t.Fatalf("raw TCP fallback wrote %d bytes", n)
	}
	if err == nil {
		t.Fatal("expected raw TCP fallback to close without HTTP response bytes")
	}
}
