package gateway_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-gatiator/internal/gateway"
)

func TestE2EWithConcurrencyLimit(t *testing.T) {
	var mockMu sync.Mutex
	var mockConcurrent int
	var mockMaxSeen int

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mockMu.Lock()
		mockConcurrent++
		if mockConcurrent > mockMaxSeen {
			mockMaxSeen = mockConcurrent
		}
		mockMu.Unlock()

		time.Sleep(100 * time.Millisecond)

		mockMu.Lock()
		mockConcurrent--
		mockMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   "test",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": "ok",
				},
				"finish_reason": "stop",
			}},
		})
	}))
	defer mockServer.Close()

	fallbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-fallback",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   "fallback",
			"choices": []map[string]any{{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": "fallback-ok",
				},
				"finish_reason": "stop",
			}},
		})
	}))
	defer fallbackServer.Close()

	tests := []struct {
		name           string
		maxConcurrent int
		numRequests    int
	}{
		{
			name:           "Sem limitação (maxConcurrent=0)",
			maxConcurrent: 0,
			numRequests:   10,
		},
		{
			name:           "Limitado a 1 (maxConcurrent=1)",
			maxConcurrent: 1,
			numRequests:   10,
		},
		{
			name:           "Limitado a 5 (maxConcurrent=5)",
			maxConcurrent: 5,
			numRequests:   10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockMu.Lock()
			mockConcurrent = 0
			mockMaxSeen = 0
			mockMu.Unlock()

			cfg := gateway.Config{
				Server: gateway.ServerConfig{Port: 0, Host: "localhost"},
				Retry:  gateway.RetryConfig{MaxAttempts: 1, DelayMs: 0},
				Providers: []gateway.ProviderConfig{
					{
						Name:          "test-mock",
						Enabled:       true,
						MaxConcurrent: tt.maxConcurrent,
						Priority:      1,
						BaseURL:       mockServer.URL,
						APIKeys:       []string{""},
						DefaultModel:  "test",
						Models:        []string{"test"},
						Headers:       map[string]string{},
					},
					{
						Name:          "fallback-mock",
						Enabled:       true,
						MaxConcurrent: 0,
						Priority:      2,
						BaseURL:       fallbackServer.URL,
						APIKeys:       []string{""},
						DefaultModel:  "test",
						Models:        []string{"test"},
						Headers:       map[string]string{},
					},
				},
			}

			gw := gateway.NewGateway(cfg)
			mux := http.NewServeMux()
			mux.HandleFunc("/v1/", gw.HandleGeneric)
			server := httptest.NewServer(mux)
			defer server.Close()

			var wg sync.WaitGroup
			var success int64
			var failed int64

			start := time.Now()

			for i := 0; i < tt.numRequests; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()

					payload := map[string]interface{}{
						"model": "test",
						"messages": []map[string]string{
							{"role": "user", "content": fmt.Sprintf("test %d", id)},
						},
						"max_tokens": 10,
					}

					body, _ := json.Marshal(payload)
					resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
					if err != nil {
						atomic.AddInt64(&failed, 1)
						fmt.Printf("[%s] [%d] ERRO: %v\n", tt.name, id, err)
						return
					}
					defer resp.Body.Close()

					io.ReadAll(resp.Body)

					if resp.StatusCode == 200 {
						atomic.AddInt64(&success, 1)
					} else {
						atomic.AddInt64(&failed, 1)
					}
				}(i)
			}

			wg.Wait()
			elapsed := time.Since(start)

			mockMu.Lock()
			peakConcurrency := mockMaxSeen
			mockMu.Unlock()

			fmt.Printf("\n=== %s ===\n", tt.name)
			fmt.Printf("maxConcurrent: %d\n", tt.maxConcurrent)
			fmt.Printf("Requests: %d | Sucessos: %d | Falhas: %d\n", tt.numRequests, success, failed)
			fmt.Printf("Tempo total: %v\n", elapsed)
			fmt.Printf("Pico de concorrência no mock: %d\n", peakConcurrency)

			if success != int64(tt.numRequests) {
				t.Errorf("Esperado %d sucessos, got %d", tt.numRequests, success)
			}

			if tt.maxConcurrent > 0 && peakConcurrency > tt.maxConcurrent {
				t.Errorf("Pico de concorrência (%d) excedeu maxConcurrent (%d)", peakConcurrency, tt.maxConcurrent)
			}
		})
	}
}