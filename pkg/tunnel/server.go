package tunnel

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/labring/sealtun/pkg/accesspolicy"
	tunnelprotocol "github.com/labring/sealtun/pkg/protocol"
	"github.com/labring/sealtun/pkg/publicauth"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

type ServerOptions struct {
	BasicAuth    *publicauth.BasicAuth
	AccessPolicy *accesspolicy.Policy
}

type Server struct {
	secret                     string
	port                       int
	protocol                   string
	localPort                  string
	basicAuth                  *publicauth.BasicAuth
	accessPolicy               *accesspolicy.Policy
	basicAuthAuthorizedHeaders sync.Map
	basicAuthCacheCount        atomic.Int64

	mu            sync.RWMutex
	activeSession *yamux.Session
	upgrader      websocket.Upgrader
	reverseProxy  *httputil.ReverseProxy
	connectedAt   atomic.Int64

	totalRequests      atomic.Int64
	activeRequests     atomic.Int64
	totalResponseBytes atomic.Int64
	total5xx           atomic.Int64
	lastStatus         atomic.Int64
	lastRequestAt      atomic.Int64
	totalDurationMs    atomic.Int64

	totalTCPConnections  atomic.Int64
	activeTCPConnections atomic.Int64
	totalTCPBytes        atomic.Int64
	totalTCPErrors       atomic.Int64
	lastTCPConnectedAt   atomic.Int64
}

func NewServer(secret string, port int, protocol string, localPort string) *Server {
	return NewServerWithOptions(secret, port, protocol, localPort, ServerOptions{})
}

func NewServerWithOptions(secret string, port int, protocol string, localPort string, opts ServerOptions) *Server {
	s := &Server{
		secret:       secret,
		port:         port,
		protocol:     protocol,
		localPort:    localPort,
		basicAuth:    opts.BasicAuth,
		accessPolicy: opts.AccessPolicy,
		upgrader: websocket.Upgrader{
			// The tunnel control/TCP WebSocket endpoints are only ever dialed by
			// the non-browser Sealtun CLI client, which never sets an Origin
			// header. Reject any request that carries one so a browser page
			// cannot be coerced into opening a tunnel WebSocket even if the
			// bearer secret were somehow exposed to client-side JavaScript.
			CheckOrigin: func(r *http.Request) bool {
				return r.Header.Get("Origin") == ""
			},
		},
	}

	director := func(req *http.Request) {
		req.URL.Scheme = "http"
		req.URL.Host = "tunnel-target"
	}

	s.reverseProxy = &httputil.ReverseProxy{
		Director:  director,
		Transport: s.reverseProxyTransport(),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			WriteUnavailablePage(w, s.localPort, fmt.Sprintf("The remote ingress is reachable, but the local Sealtun client is not connected to this tunnel yet: %v", err))
		},
	}

	return s
}

func (s *Server) reverseProxyTransport() http.RoundTripper {
	return &http.Transport{
		DialContext:           s.proxyDialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func (s *Server) proxyDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	s.mu.RLock()
	session := s.activeSession
	s.mu.RUnlock()

	if session == nil || session.IsClosed() {
		return nil, fmt.Errorf("local client is not connected")
	}

	return session.OpenStream()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/_sealtun/healthz" {
		s.handleHealthz(w, r)
		return
	}
	if r.URL.Path == "/_sealtun/metrics" {
		s.handleMetrics(w, r)
		return
	}

	// 1. Check if it's the internal tunnel negotiation endpoint
	if r.URL.Path == "/_sealtun/ws" {
		s.handleTunnelConnection(w, r)
		return
	}
	if r.URL.Path == "/_sealtun/tcp" {
		s.handleTCPConnection(w, r)
		return
	}

	// 2. All other requests are public traffic -> Forward to Local Client via Reverse Proxy
	s.handlePublicTraffic(w, r)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if !requireReadOnlyMethod(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.mu.RLock()
	connected := s.activeSession != nil && !s.activeSession.IsClosed()
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	if !connected {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintf(w, `{"ok":false,"clientConnected":false,"protocol":%q}`, s.protocol)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `{"ok":true,"clientConnected":true,"protocol":%q,"connectedAt":%q}`, s.protocol, time.Unix(s.connectedAt.Load(), 0).Format(time.RFC3339))
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !requireReadOnlyMethod(w, r) {
		return
	}
	if !s.authorized(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	s.mu.RLock()
	connected := s.activeSession != nil && !s.activeSession.IsClosed()
	s.mu.RUnlock()

	connectedAt := ""
	if value := s.connectedAt.Load(); value > 0 {
		connectedAt = time.Unix(value, 0).Format(time.RFC3339)
	}
	lastRequestAt := ""
	if value := s.lastRequestAt.Load(); value > 0 {
		lastRequestAt = time.Unix(value, 0).Format(time.RFC3339)
	}

	total := s.totalRequests.Load()
	avgDurationMs := int64(0)
	if total > 0 {
		avgDurationMs = s.totalDurationMs.Load() / total
	}
	payload := map[string]interface{}{
		"clientConnected":     connected,
		"connectedAt":         connectedAt,
		"protocol":            s.protocol,
		"localPort":           s.localPort,
		"totalRequests":       total,
		"activeRequests":      s.activeRequests.Load(),
		"totalResponseBytes":  s.totalResponseBytes.Load(),
		"total5xx":            s.total5xx.Load(),
		"lastStatus":          s.lastStatus.Load(),
		"lastRequestAt":       lastRequestAt,
		"averageDurationMs":   avgDurationMs,
		"totalDurationMillis": s.totalDurationMs.Load(),
	}
	if tunnelprotocol.UsesRawTCP(s.protocol) {
		lastTCPConnectedAt := ""
		if value := s.lastTCPConnectedAt.Load(); value > 0 {
			lastTCPConnectedAt = time.Unix(value, 0).Format(time.RFC3339)
		}
		payload["totalTCPConnections"] = s.totalTCPConnections.Load()
		payload["activeTCPConnections"] = s.activeTCPConnections.Load()
		payload["totalTCPBytes"] = s.totalTCPBytes.Load()
		payload["totalTCPErrors"] = s.totalTCPErrors.Load()
		payload["lastTCPConnectedAt"] = lastTCPConnectedAt
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func requireReadOnlyMethod(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	return false
}

func (s *Server) handlePublicTraffic(w http.ResponseWriter, r *http.Request) {
	if ok, reason := accesspolicy.NetworkAllowed(s.accessPolicy, r); !ok {
		http.Error(w, reason, http.StatusForbidden)
		return
	}
	authKind, ok := s.authorizedPublicTraffic(r)
	if !ok {
		if s.basicAuth != nil {
			w.Header().Add("WWW-Authenticate", `Basic realm="Sealtun Tunnel", charset="UTF-8"`)
		}
		if accesspolicy.HasTokenAuth(s.accessPolicy) {
			w.Header().Add("WWW-Authenticate", `Bearer realm="Sealtun Tunnel"`)
		}
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	accesspolicy.StripTemporaryTokenQuery(r.URL)
	if authKind == publicAuthBasic || authKind == publicAuthBearer {
		r.Header.Del("Authorization")
	}

	start := time.Now()
	s.totalRequests.Add(1)
	s.activeRequests.Add(1)
	defer s.activeRequests.Add(-1)

	recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	s.reverseProxy.ServeHTTP(recorder, r)

	status := recorder.status
	s.lastStatus.Store(int64(status))
	s.lastRequestAt.Store(time.Now().Unix())
	s.totalResponseBytes.Add(int64(recorder.bytes))
	s.totalDurationMs.Add(time.Since(start).Milliseconds())
	if status >= 500 {
		s.total5xx.Add(1)
	}
	fmt.Printf("request method=%s path=%q status=%d bytes=%d duration=%s\n", r.Method, redactedRequestPath(r), status, recorder.bytes, time.Since(start).Round(time.Millisecond))
}

type publicAuthKind int

const (
	publicAuthNone publicAuthKind = iota
	publicAuthBasic
	publicAuthBearer
	publicAuthTemporary
)

func (s *Server) authorizedPublicTraffic(r *http.Request) (publicAuthKind, bool) {
	requiresBasic := s.basicAuth != nil
	requiresToken := accesspolicy.HasTokenAuth(s.accessPolicy)
	if !requiresBasic && !requiresToken {
		return publicAuthNone, true
	}
	if requiresBasic && s.authorizedBasicAuth(r) {
		return publicAuthBasic, true
	}
	if requiresToken && accesspolicy.BearerTokenAuthorized(s.accessPolicy, r) {
		return publicAuthBearer, true
	}
	if requiresToken && accesspolicy.TokenAuthorized(s.accessPolicy, r, time.Now()) {
		return publicAuthTemporary, true
	}
	return publicAuthNone, false
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
	bytes       int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	n, err := r.ResponseWriter.Write(data)
	r.bytes += n
	return n, err
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func redactedRequestPath(r *http.Request) string {
	if r == nil || r.URL == nil {
		return ""
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	if r.URL.RawQuery != "" {
		return path + "?<redacted>"
	}
	return path
}

func (s *Server) handleTunnelConnection(w http.ResponseWriter, r *http.Request) {
	// Authenticate
	if !s.authorized(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Upgrade
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("upgrade error: %v\n", err)
		return
	}
	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(45 * time.Second))
	})
	stopPing := make(chan struct{})
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
					_ = conn.Close()
					return
				}
			case <-stopPing:
				return
			}
		}
	}()
	defer close(stopPing)

	netConn := NewWSConn(conn)

	// Since we OPEN streams to the client, we act as the Yamux Client!
	yamuxConfig := yamux.DefaultConfig()
	yamuxConfig.EnableKeepAlive = true
	yamuxConfig.KeepAliveInterval = 10 * time.Second

	session, err := yamux.Client(netConn, yamuxConfig)
	if err != nil {
		fmt.Printf("yamux client setup error: %v\n", err)
		_ = netConn.Close()
		return
	}

	// Replace active session. Close the previous session after a short grace
	// period instead of immediately, so in-flight requests on the old session
	// can drain rather than being abruptly cut to a 502. This also makes
	// reconnection storms less disruptive to active traffic.
	s.mu.Lock()
	old := s.activeSession
	s.activeSession = session
	s.connectedAt.Store(time.Now().Unix())
	s.mu.Unlock()
	if old != nil && !old.IsClosed() {
		go func() {
			time.Sleep(sessionDrainGrace)
			_ = old.Close()
		}()
	}

	fmt.Println("Local client connected successfully to the server pod.")

	// Wait for the session to close before exiting the handler
	<-session.CloseChan()
	fmt.Println("Local client disconnected.")
}

func (s *Server) handleTCPConnection(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	s.mu.RLock()
	session := s.activeSession
	s.mu.RUnlock()
	if session == nil || session.IsClosed() {
		http.Error(w, "local client is not connected", http.StatusServiceUnavailable)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("tcp upgrade error: %v\n", err)
		return
	}
	defer conn.Close()

	stream, err := session.OpenStream()
	if err != nil {
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "local client is not connected"))
		return
	}
	defer stream.Close()

	relayWebSocketAndStream(conn, stream)
}

func relayWebSocketAndStream(conn *websocket.Conn, stream net.Conn) {
	wsConn := NewWSConn(conn)
	defer wsConn.Close()

	errc := make(chan error, 2)
	go func() {
		_, err := io.Copy(stream, wsConn)
		errc <- err
	}()
	go func() {
		_, err := io.Copy(wsConn, stream)
		errc <- err
	}()
	<-errc
}

func (s *Server) authorized(r *http.Request) bool {
	authHeader := r.Header.Get("Authorization")
	expectedAuth := fmt.Sprintf("Bearer %s", s.secret)
	return subtle.ConstantTimeCompare([]byte(authHeader), []byte(expectedAuth)) == 1
}

func (s *Server) authorizedBasicAuth(r *http.Request) bool {
	if s.basicAuth == nil {
		return true
	}
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		if _, ok := s.basicAuthAuthorizedHeaders.Load(basicAuthHeaderDigest(authHeader)); ok {
			return true
		}
	}
	username, password, ok := r.BasicAuth()
	if !ok {
		return false
	}
	authorized := publicauth.Check(*s.basicAuth, username, password)
	if authorized && authHeader != "" {
		s.cacheBasicAuthHeader(authHeader)
	}
	return authorized
}

// maxBasicAuthCacheEntries bounds the positive-auth header cache so a client
// cannot exhaust memory by sending many distinct-but-valid Authorization
// headers (Base64 has multiple encodings/padding for the same credentials).
const maxBasicAuthCacheEntries = 1024

// sessionDrainGrace is how long a superseded tunnel session is kept open after
// a new client connects, so in-flight requests can finish instead of being cut.
const sessionDrainGrace = 5 * time.Second

func (s *Server) cacheBasicAuthHeader(authHeader string) {
	if s.basicAuthCacheCount.Load() >= maxBasicAuthCacheEntries {
		// Simple bounded-cache eviction: drop everything and start over once
		// the cap is reached. The cost is at most one extra bcrypt verification
		// per still-active client after a flush, which is acceptable.
		s.basicAuthAuthorizedHeaders.Range(func(key, _ any) bool {
			s.basicAuthAuthorizedHeaders.Delete(key)
			return true
		})
		s.basicAuthCacheCount.Store(0)
	}
	if _, loaded := s.basicAuthAuthorizedHeaders.LoadOrStore(basicAuthHeaderDigest(authHeader), struct{}{}); !loaded {
		s.basicAuthCacheCount.Add(1)
	}
}

func basicAuthHeaderDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
	fmt.Printf("Server listening on %s (H2C enabled)\n", addr)

	errc := make(chan error, 2)
	if tunnelprotocol.UsesRawTCP(s.protocol) {
		go func() {
			errc <- s.startRawTCPListener(2222)
		}()
	}

	h2s := &http2.Server{}
	server := &http.Server{
		Addr:              addr,
		Handler:           h2c.NewHandler(s, h2s),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		errc <- server.ListenAndServe()
	}()
	return <-errc
}

func (s *Server) startRawTCPListener(port int) error {
	addr := fmt.Sprintf(":%d", port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen raw tcp on %s: %w", addr, err)
	}
	defer listener.Close()
	fmt.Printf("Raw TCP listener enabled on %s\n", addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go s.handleRawTCPConnection(conn)
	}
}

func (s *Server) handleRawTCPConnection(conn net.Conn) {
	defer conn.Close()
	s.totalTCPConnections.Add(1)
	s.activeTCPConnections.Add(1)
	s.lastTCPConnectedAt.Store(time.Now().Unix())
	defer s.activeTCPConnections.Add(-1)

	s.mu.RLock()
	session := s.activeSession
	s.mu.RUnlock()
	if session == nil || session.IsClosed() {
		s.totalTCPErrors.Add(1)
		return
	}

	stream, err := session.OpenStream()
	if err != nil {
		s.totalTCPErrors.Add(1)
		return
	}
	defer stream.Close()

	if err := s.relayRawTCPConns(conn, stream); err != nil && !expectedRelayClose(err) {
		s.totalTCPErrors.Add(1)
	}
}

func (s *Server) relayRawTCPConns(a, b net.Conn) error {
	errc := make(chan error, 2)
	go func() {
		n, err := io.Copy(a, b)
		s.totalTCPBytes.Add(n)
		errc <- err
	}()
	go func() {
		n, err := io.Copy(b, a)
		s.totalTCPBytes.Add(n)
		errc <- err
	}()
	return <-errc
}

func expectedRelayClose(err error) bool {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	return strings.Contains(err.Error(), "use of closed network connection") ||
		strings.Contains(err.Error(), "websocket: close")
}
