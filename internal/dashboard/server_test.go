package dashboard

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// freePort returns an available localhost port.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func TestHandleStatus_NoProvider(t *testing.T) {
	s := New(0)

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	s.handleStatus(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status code = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var status Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if status.State != "starting" {
		t.Errorf("State = %q, want starting", status.State)
	}
}

func TestHandleStatus_WithProvider(t *testing.T) {
	s := New(0)

	want := Status{
		NodeID:  "node-abc",
		Version: "v0.1.0",
		State:   "earning",
		Model:   "llama3:8b",
	}
	want.Earnings.TodayUSD = 1.23
	want.Earnings.TotalUSD = 9.99
	want.GPU.Name = "RTX 4090"
	want.GPU.UtilPct = 42
	want.Gateway.Connected = true
	want.Gateway.JobsToday = 5

	s.SetProvider(func() Status { return want })

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	s.handleStatus(w, req)

	var got Status
	if err := json.NewDecoder(w.Result().Body).Decode(&got); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if got.NodeID != want.NodeID {
		t.Errorf("NodeID = %q, want %q", got.NodeID, want.NodeID)
	}
	if got.State != "earning" {
		t.Errorf("State = %q, want earning", got.State)
	}
	if got.Earnings.TodayUSD != 1.23 {
		t.Errorf("TodayUSD = %f, want 1.23", got.Earnings.TodayUSD)
	}
	if got.Earnings.TotalUSD != 9.99 {
		t.Errorf("TotalUSD = %f, want 9.99", got.Earnings.TotalUSD)
	}
	if got.GPU.Name != "RTX 4090" {
		t.Errorf("GPU.Name = %q", got.GPU.Name)
	}
	if got.GPU.UtilPct != 42 {
		t.Errorf("GPU.UtilPct = %d, want 42", got.GPU.UtilPct)
	}
	if !got.Gateway.Connected {
		t.Error("Gateway.Connected should be true")
	}
	if got.Gateway.JobsToday != 5 {
		t.Errorf("Gateway.JobsToday = %d, want 5", got.Gateway.JobsToday)
	}
}

func TestHandleStatus_CORSHeader(t *testing.T) {
	s := New(0)
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	w := httptest.NewRecorder()
	s.handleStatus(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
}

func TestHandleIndex_ServesHTML(t *testing.T) {
	s := New(0)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	s.handleIndex(w, req)

	resp := w.Result()
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if w.Body.Len() == 0 {
		t.Error("body is empty")
	}
}

func TestNew_DefaultPort(t *testing.T) {
	s := New(0)
	if s.port != 8080 {
		t.Errorf("port = %d, want 8080", s.port)
	}
}

func TestNew_CustomPort(t *testing.T) {
	s := New(9090)
	if s.port != 9090 {
		t.Errorf("port = %d, want 9090", s.port)
	}
}

func TestStart_ListensAndResponds(t *testing.T) {
	port := freePort(t)
	s := New(port)
	s.SetProvider(func() Status {
		return Status{State: "idle", Version: "test"}
	})
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/status", port))
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var status Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.State != "idle" {
		t.Errorf("State = %q, want idle", status.State)
	}
}
