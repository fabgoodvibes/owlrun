// Mock Ollama server for debugging without real models.
// Listens on :11434 and responds to the Ollama API endpoints with dummy data.
// Enabled via --mock flag. Useful for testing the full pipeline without
// downloading any models (e.g., airplane wifi, CI, demos).
package inference

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

const mockModel = "mock:latest"

// MockServer is a fake Ollama API server for debugging.
type MockServer struct {
	ln   net.Listener
	Addr string // actual listen address, e.g. "127.0.0.1:11434"
}

// StartMockOllama starts a mock Ollama server on :11434.
// If 11434 is already in use (real Ollama running), it picks a free port.
// Returns after the listener is bound (server runs in background).
func StartMockOllama() (*MockServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:11434")
	if err != nil {
		// Port in use (likely real Ollama) — pick a free port.
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("mock ollama: bind: %w", err)
		}
	}

	addr := ln.Addr().String()
	mux := http.NewServeMux()
	ms := &MockServer{ln: ln, Addr: addr}

	mux.HandleFunc("/api/tags", ms.handleTags)
	mux.HandleFunc("/api/generate", ms.handleGenerate)
	mux.HandleFunc("/api/chat", ms.handleChat)
	mux.HandleFunc("/v1/chat/completions", ms.handleChatCompletions)
	mux.HandleFunc("/api/pull", ms.handlePull)
	mux.HandleFunc("/api/delete", ms.handleDelete)
	mux.HandleFunc("/", ms.handleRoot)

	go func() {
		srv := &http.Server{Handler: mux}
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("owlrun: mock ollama server error: %v", err)
		}
	}()

	log.Printf("owlrun: mock ollama server running on %s", addr)
	return ms, nil
}

// Stop shuts down the mock server.
func (ms *MockServer) Stop() {
	if ms.ln != nil {
		ms.ln.Close()
	}
}

// MockModel returns the tag of the fake model.
func MockModel() string {
	return mockModel
}

func (ms *MockServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, "Ollama is running (mock)")
}

func (ms *MockServer) handleTags(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"models": []map[string]any{
			{
				"name":        mockModel,
				"model":       mockModel,
				"modified_at": time.Now().UTC().Format(time.RFC3339),
				"size":        1024,
				"details": map[string]any{
					"format":            "gguf",
					"family":            "mock",
					"parameter_size":    "0B",
					"quantization_level": "Q4_0",
				},
			},
		},
	})
}

func (ms *MockServer) handleGenerate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"model":              mockModel,
		"response":           "",
		"done":               true,
		"total_duration":     1000000,
		"load_duration":      500000,
		"prompt_eval_count":  0,
		"eval_count":         0,
	})
}

func (ms *MockServer) handleChat(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Transfer-Encoding", "chunked")
	flusher, _ := w.(http.Flusher)

	// Stream a few tokens then done.
	words := []string{"This ", "is ", "a ", "mock ", "response ", "from ", "Owlrun ", "debug ", "mode."}
	for _, word := range words {
		chunk, _ := json.Marshal(map[string]any{
			"model": mockModel,
			"message": map[string]string{
				"role":    "assistant",
				"content": word,
			},
			"done": false,
		})
		w.Write(chunk)
		w.Write([]byte("\n"))
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Final chunk with done=true and token counts.
	final, _ := json.Marshal(map[string]any{
		"model": mockModel,
		"message": map[string]string{
			"role":    "assistant",
			"content": "",
		},
		"done":               true,
		"total_duration":     450000000,
		"load_duration":      1000000,
		"prompt_eval_count":  10,
		"eval_count":         int(len(words)),
		"prompt_eval_duration": 100000000,
		"eval_duration":      350000000,
	})
	w.Write(final)
	w.Write([]byte("\n"))
	if flusher != nil {
		flusher.Flush()
	}
}

func (ms *MockServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)

	// Stream SSE chunks in OpenAI format.
	words := []string{"This ", "is ", "a ", "mock ", "response ", "from ", "Owlrun ", "debug ", "mode."}
	for i, word := range words {
		chunk, _ := json.Marshal(map[string]any{
			"id":      fmt.Sprintf("chatcmpl-mock-%d", i),
			"object":  "chat.completion.chunk",
			"model":   mockModel,
			"choices": []map[string]any{{
				"index": 0,
				"delta": map[string]string{"content": word},
			}},
		})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Final chunk with finish_reason + usage.
	final, _ := json.Marshal(map[string]any{
		"id":      "chatcmpl-mock-final",
		"object":  "chat.completion.chunk",
		"model":   mockModel,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]string{},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{
			"prompt_tokens":     10,
			"completion_tokens": len(words),
			"total_tokens":      10 + len(words),
		},
	})
	fmt.Fprintf(w, "data: %s\n\n", final)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (ms *MockServer) handlePull(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, _ := w.(http.Flusher)

	statuses := []string{"pulling manifest", "pulling sha256:mock", "verifying sha256 digest", "writing manifest", "success"}
	for _, s := range statuses {
		chunk, _ := json.Marshal(map[string]any{
			"status": s,
		})
		w.Write(chunk)
		w.Write([]byte("\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func (ms *MockServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}
