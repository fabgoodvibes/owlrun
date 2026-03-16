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
)

// startTLSGateway starts a TLS+HTTP/2 server simulating the gateway Option A
// rendezvous endpoints:
//
//	GET  /.../proxy/request  — returns buyerRequest as body, then returns (clean EOF)
//	POST /.../proxy/response — reads Ollama's response from the node into gotNodeBody
//
// waitGW blocks until the POST handler has completed. It is safe to call even
// if the POST never arrives (times out instead of hanging).
func startTLSGateway(t *testing.T, buyerRequest string, gotNodeBody *strings.Builder) (*httptest.Server, func()) {
	t.Helper()
	postDone := make(chan struct{})
	var once sync.Once
	closePostDone := func() { once.Do(func() { close(postDone) }) }

	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		switch {
		case strings.HasSuffix(r.URL.Path, "/proxy/request") && r.Method == http.MethodGet:
			// Return buyer's request as response body then return immediately.
			// Handler return → HTTP/2 END_STREAM → clean EOF for the node.
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, buyerRequest)

		case strings.HasSuffix(r.URL.Path, "/proxy/response") && r.Method == http.MethodPost:
			// Receive Ollama's response from the node.
			if gotNodeBody != nil {
				b, _ := io.ReadAll(r.Body)
				gotNodeBody.Write(b)
			} else {
				io.Copy(io.Discard, r.Body)
			}
			w.WriteHeader(http.StatusOK)
			closePostDone()

		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	ts.EnableHTTP2 = true
	ts.StartTLS()
	t.Cleanup(ts.Close)

	waitGW := func() {
		select {
		case <-postDone:
		case <-time.After(5 * time.Second):
		}
	}
	return ts, waitGW
}

// startOllama starts a plain HTTP server that acts as Ollama.
// io.ReadAll is now safe: buyer's request body has a clean EOF because the
// GET /proxy/request handler returned (END_STREAM) before we forward to Ollama.
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

	var nodeBody strings.Builder
	gw, waitGW := startTLSGateway(t, buyerReq, &nodeBody)
	ollama := startOllama(t, buyerReq, ollamaOut)
	c := connector(gw, ollama.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.proxyJob(ctx, "job-123"); err != nil {
		t.Fatalf("proxyJob error: %v", err)
	}
	waitGW() // wait for POST handler to finish writing into nodeBody
	if got := nodeBody.String(); got != ollamaOut {
		t.Errorf("gateway received node body %q, want %q", got, ollamaOut)
	}
}

func TestProxyJob_GatewayNonOK(t *testing.T) {
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "job not found", http.StatusNotFound)
	}))
	ts.EnableHTTP2 = true
	ts.StartTLS()
	defer ts.Close()

	c := &Connector{
		gatewayBase:   ts.URL,
		apiKey:        "test-key",
		gatewayClient: ts.Client(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := c.proxyJob(ctx, "job-404")
	if err == nil {
		t.Fatal("expected error from 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}

func TestProxyJob_OllamaError(t *testing.T) {
	const buyerReq = `{"model":"llama3:8b"}`

	gw, _ := startTLSGateway(t, buyerReq, nil)

	badOllama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not loaded", http.StatusInternalServerError)
	}))
	defer badOllama.Close()

	c := connector(gw, badOllama.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := c.proxyJob(ctx, "job-ollamaerr")
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

	c := &Connector{
		gatewayBase:   ts.URL,
		apiKey:        "test-key",
		gatewayClient: ts.Client(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.proxyJob(ctx, "job-hang") }()

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
	c := &Connector{
		gatewayBase: "https://127.0.0.1:19999",
		apiKey:      "key",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := c.proxyJob(ctx, "job-noconn"); err == nil {
		t.Fatal("expected connection error, got nil")
	}
}
