package dashboard

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsValidLightningAddress(t *testing.T) {
	valid := []string{
		"user@walletofsatoshi.com",
		"alice@getalby.com",
		"test@domain.co.uk",
		"a@b.c",
	}
	for _, addr := range valid {
		if !isValidLightningAddress(addr) {
			t.Errorf("isValidLightningAddress(%q) = false, want true", addr)
		}
	}

	invalid := []string{
		"",
		"@",
		"user@",
		"@domain.com",
		"nodomain",
		"user@@domain.com",
		"user@domain",      // no TLD dot
		"user@.com",        // empty subdomain
		"user@domain..com", // consecutive dots
		"user @domain.com", // space
		"user\t@domain.com",
		"user\n@domain.com",
	}
	for _, addr := range invalid {
		if isValidLightningAddress(addr) {
			t.Errorf("isValidLightningAddress(%q) = true, want false", addr)
		}
	}
}

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
	if s.port != 19131 {
		t.Errorf("port = %d, want 19131", s.port)
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

func TestHandleSetKeepWarm_NotReady(t *testing.T) {
	s := New(0)
	body := `{"on":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/set-keep-warm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleSetKeepWarm(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestHandleSetKeepWarm_MethodNotAllowed(t *testing.T) {
	s := New(0)
	req := httptest.NewRequest(http.MethodGet, "/api/set-keep-warm", nil)
	w := httptest.NewRecorder()
	s.handleSetKeepWarm(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestHandleSetKeepWarm_Enable(t *testing.T) {
	s := New(0)
	var called bool
	var gotValue bool
	fn := SetKeepWarmFunc(func(on bool) error {
		called = true
		gotValue = on
		return nil
	})
	s.SetKeepWarmSetter(fn)

	body := `{"on":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/set-keep-warm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleSetKeepWarm(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if !called {
		t.Error("setter was not called")
	}
	if !gotValue {
		t.Error("setter received false, want true")
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["keep_warm"] != true {
		t.Errorf("response keep_warm = %v, want true", resp["keep_warm"])
	}
}

func TestHandleSetKeepWarm_Disable(t *testing.T) {
	s := New(0)
	var gotValue bool
	fn := SetKeepWarmFunc(func(on bool) error {
		gotValue = on
		return nil
	})
	s.SetKeepWarmSetter(fn)

	body := `{"on":false}`
	req := httptest.NewRequest(http.MethodPost, "/api/set-keep-warm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleSetKeepWarm(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if gotValue {
		t.Error("setter received true, want false")
	}
}

func TestHandleSetKeepWarm_Error(t *testing.T) {
	s := New(0)
	fn := SetKeepWarmFunc(func(on bool) error {
		return fmt.Errorf("disk full")
	})
	s.SetKeepWarmSetter(fn)

	body := `{"on":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/set-keep-warm", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleSetKeepWarm(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

func TestHandleSetKeepWarm_InvalidJSON(t *testing.T) {
	s := New(0)
	fn := SetKeepWarmFunc(func(on bool) error { return nil })
	s.SetKeepWarmSetter(fn)

	req := httptest.NewRequest(http.MethodPost, "/api/set-keep-warm", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleSetKeepWarm(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
