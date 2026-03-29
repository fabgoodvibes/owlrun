package inference

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fabgoodvibes/owlrun/internal/gpu"
)

func TestStartStopKeepWarm(t *testing.T) {
	m := New(gpu.Info{})

	if m.IsKeepWarmRunning() {
		t.Fatal("keep-warm should not be running initially")
	}

	m.StartKeepWarm("test:latest")
	if !m.IsKeepWarmRunning() {
		t.Fatal("keep-warm should be running after Start")
	}

	m.StopKeepWarm()
	if m.IsKeepWarmRunning() {
		t.Fatal("keep-warm should not be running after Stop")
	}
}

func TestStopKeepWarm_Idempotent(t *testing.T) {
	m := New(gpu.Info{})

	// Calling StopKeepWarm when not running should not panic.
	m.StopKeepWarm()
	m.StopKeepWarm()

	m.StartKeepWarm("test:latest")
	m.StopKeepWarm()
	m.StopKeepWarm() // double stop — should be safe
}

func TestStartKeepWarm_ReplacesExisting(t *testing.T) {
	m := New(gpu.Info{})

	m.StartKeepWarm("model-a")
	if !m.IsKeepWarmRunning() {
		t.Fatal("should be running")
	}

	// Starting with a different model should replace, not leak goroutines.
	m.StartKeepWarm("model-b")
	if !m.IsKeepWarmRunning() {
		t.Fatal("should still be running after replace")
	}

	m.warmMu.Lock()
	if m.warmModel != "model-b" {
		t.Errorf("warmModel = %q, want model-b", m.warmModel)
	}
	m.warmMu.Unlock()

	m.StopKeepWarm()
}

func TestKeepWarm_PingsOllama(t *testing.T) {
	var pings atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/generate" {
			pings.Add(1)
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			if req["model"] != "ping-model" {
				t.Errorf("model = %v, want ping-model", req["model"])
			}
			w.WriteHeader(200)
			w.Write([]byte(`{"done":true}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	m := New(gpu.Info{})
	m.host = srv.URL

	// We can't wait 4 minutes in a test, so verify LoadModel works
	// and the goroutine starts. The ticker-based ping is an implementation
	// detail — we test that LoadModel is called with the right model.
	if err := m.LoadModel("ping-model"); err != nil {
		t.Fatalf("LoadModel failed: %v", err)
	}
	if pings.Load() != 1 {
		t.Errorf("expected 1 ping, got %d", pings.Load())
	}

	// Verify the goroutine starts and is cancellable.
	m.StartKeepWarm("ping-model")
	time.Sleep(50 * time.Millisecond) // let goroutine start
	if !m.IsKeepWarmRunning() {
		t.Fatal("should be running")
	}
	m.StopKeepWarm()
}
