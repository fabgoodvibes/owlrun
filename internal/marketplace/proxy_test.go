package marketplace

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// startGatewayHTTP starts a TLS+HTTP/2 server simulating the gateway's
// GET /proxy/request endpoint. The old POST /proxy/response endpoint is no
// longer needed — response streaming goes over WS now.
func startGatewayHTTP(t *testing.T, buyerRequest string) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		if strings.HasSuffix(r.URL.Path, "/proxy/request") && r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, buyerRequest)
			return
		}

		http.Error(w, "unexpected request", http.StatusNotFound)
	}))
	ts.EnableHTTP2 = true
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}

// wsCapture holds captured proxy_chunk data and signals when proxy_done arrives.
type wsCapture struct {
	mu       sync.Mutex
	chunks   []string
	doneCh   chan struct{}
	doneOnce sync.Once
}

func newWSCapture() *wsCapture {
	return &wsCapture{doneCh: make(chan struct{})}
}

func (c *wsCapture) Chunks() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.chunks, "")
}

// startWSServer starts a WebSocket server that reads proxy_chunk/proxy_done
// messages from the node (test connector). Returns the server and a wsCapture
// to inspect received data.
func startWSServer(t *testing.T) (*httptest.Server, *wsCapture) {
	t.Helper()
	cap := newWSCapture()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("ws accept: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		for {
			var msg wsMsg
			if err := wsjson.Read(r.Context(), conn, &msg); err != nil {
				return // connection closed
			}
			switch msg.Type {
			case "proxy_chunk":
				cap.mu.Lock()
				cap.chunks = append(cap.chunks, msg.Data)
				cap.mu.Unlock()
			case "proxy_done":
				cap.doneOnce.Do(func() { close(cap.doneCh) })
				return
			}
		}
	}))
	t.Cleanup(ts.Close)
	return ts, cap
}

// dialWS dials into the WS test server and returns a client-side conn.
func dialWS(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + url[len("http"):]
	conn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })
	return conn
}

// startOllama starts a plain HTTP server that acts as Ollama.
func startOllama(t *testing.T, wantBody, response string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("ollama path = %q, want /api/chat", r.URL.Path)
		}
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ollama ReadAll: %v", err)
		}
		if got := string(b); got != wantBody {
			t.Errorf("ollama received %q, want %q", got, wantBody)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, response)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// connector returns a Connector configured for the test servers.
func connector(gw *httptest.Server, ollamaURL string) *Connector {
	return &Connector{
		gatewayBase:   gw.URL,
		apiKey:        "test-key",
		gatewayClient: gw.Client(),
		ollamaBase:    ollamaURL,
	}
}

// -- Tests --------------------------------------------------------------------

func TestProxyJob_HappyPath(t *testing.T) {
	const buyerReq = `{"model":"llama3:8b","messages":[{"role":"user","content":"hi"}]}`
	const ollamaOut = `{"message":{"content":"hello"},"done":true,"eval_count":5}`

	gw := startGatewayHTTP(t, buyerReq)
	ollama := startOllama(t, buyerReq, ollamaOut)
	wsSrv, cap := startWSServer(t)
	wsConn := dialWS(t, wsSrv.URL)

	c := connector(gw, ollama.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.proxyJob(ctx, wsConn, "job-123"); err != nil {
		t.Fatalf("proxyJob error: %v", err)
	}

	// Wait for proxy_done.
	select {
	case <-cap.doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for proxy_done")
	}

	if got := cap.Chunks(); got != ollamaOut {
		t.Errorf("WS received %q, want %q", got, ollamaOut)
	}
}

func TestProxyJob_GatewayNonOK(t *testing.T) {
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "job not found", http.StatusNotFound)
	}))
	ts.EnableHTTP2 = true
	ts.StartTLS()
	defer ts.Close()

	wsSrv, _ := startWSServer(t)
	wsConn := dialWS(t, wsSrv.URL)

	c := &Connector{
		gatewayBase:   ts.URL,
		apiKey:        "test-key",
		gatewayClient: ts.Client(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := c.proxyJob(ctx, wsConn, "job-404")
	if err == nil {
		t.Fatal("expected error from 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}

func TestProxyJob_OllamaError(t *testing.T) {
	const buyerReq = `{"model":"llama3:8b"}`

	gw := startGatewayHTTP(t, buyerReq)
	wsSrv, _ := startWSServer(t)
	wsConn := dialWS(t, wsSrv.URL)

	badOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusInternalServerError)
	}))
	defer badOllama.Close()

	c := connector(gw, badOllama.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := c.proxyJob(ctx, wsConn, "job-ollamaerr")
	if err == nil {
		t.Fatal("expected error from ollama 500, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500, got: %v", err)
	}
}

func TestProxyJob_ContextCancel(t *testing.T) {
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	ts.EnableHTTP2 = true
	ts.StartTLS()
	defer ts.Close()

	wsSrv, _ := startWSServer(t)
	wsConn := dialWS(t, wsSrv.URL)

	c := &Connector{
		gatewayBase:   ts.URL,
		apiKey:        "test-key",
		gatewayClient: ts.Client(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.proxyJob(ctx, wsConn, "job-hang") }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("expected error after context cancel, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Error("proxyJob did not return after context cancel")
	}
}

func TestProxyJob_GatewayConnectError(t *testing.T) {
	wsSrv, _ := startWSServer(t)
	wsConn := dialWS(t, wsSrv.URL)

	c := &Connector{
		gatewayBase: "https://127.0.0.1:19999",
		apiKey:      "key",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := c.proxyJob(ctx, wsConn, "job-noconn"); err == nil {
		t.Fatal("expected connection error, got nil")
	}
}
