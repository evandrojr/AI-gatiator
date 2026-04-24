package gateway_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-gatiator/internal/gateway"
)

type mockProvider struct {
	name      string
	maxConc   int
	latency   time.Duration
	failCount int
	mu        sync.Mutex
}

func (m *mockProvider) handleChat(w http.ResponseWriter, r *http.Request) {
	if m.maxConc > 0 {
		m.mu.Lock()
		if m.failCount > 0 {
			m.failCount--
			m.mu.Unlock()
			http.Error(w, `{"error":"overloaded"}`, 503)
			return
		}
		m.mu.Unlock()
	}

	time.Sleep(m.latency)
	json.NewEncoder(w).Encode(map[string]any{
		"model": "test",
		"choices": []map[string]any{{
			"message": map[string]string{"role": "assistant", "content": "ok"},
		}},
	})
}

func createMockServer(latency time.Duration, maxConcurrent int) *httptest.Server {
	m := &mockProvider{
		name:      "mock",
		latency:   latency,
		maxConc:   maxConcurrent,
		failCount: 0,
	}

	return httptest.NewServer(http.HandlerFunc(m.handleChat))
}

func TestConcurrencySemaphore(t *testing.T) {
	tests := []struct {
		name           string
		maxConcurrent  int
		numRequests    int
		providerLatency time.Duration
	}{
		{
			name:           "Sem limitação (maxConcurrent=0)",
			maxConcurrent:  0,
			numRequests:    10,
			providerLatency: 100 * time.Millisecond,
		},
		{
			name:           "Limitado a 1 (maxConcurrent=1)",
			maxConcurrent:  1,
			numRequests:    10,
			providerLatency: 100 * time.Millisecond,
		},
		{
			name:           "Limitado a 3 (maxConcurrent=3)",
			maxConcurrent:  3,
			numRequests:    10,
			providerLatency: 100 * time.Millisecond,
		},
		{
			name:           "Limitado a 10 (maxConcurrent=10)",
			maxConcurrent:  10,
			numRequests:    10,
			providerLatency: 100 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := createMockServer(tt.providerLatency, tt.maxConcurrent)
			fallbackServer := createMockServer(50*time.Millisecond, 0)
			defer server.Close()
			defer fallbackServer.Close()

			cfg := gateway.Config{
				Retry: gateway.RetryConfig{MaxAttempts: 1, DelayMs: 0},
				Providers: []gateway.ProviderConfig{
					{
						Name:          "test-provider",
						Enabled:       true,
						MaxConcurrent: tt.maxConcurrent,
						Priority:      1,
						BaseURL:       server.URL,
						APIKeys:       []string{""},
						DefaultModel:  "test",
						Models:        []string{"test"},
						Headers:       map[string]string{},
					},
					{
						Name:          "fallback-provider",
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

			var wg sync.WaitGroup
			var success int64
			var failed int64

			start := time.Now()

			for i := 0; i < tt.numRequests; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()

					reqMap := map[string]any{
						"model": "test",
						"messages": []map[string]string{
							{"role": "user", "content": "test"},
						},
					}

					rr := httptest.NewRecorder()
					gw.Forward(reqMap, "test", rr, "/v1/chat/completions")

					if rr.Code == 200 {
						atomic.AddInt64(&success, 1)
					} else {
						atomic.AddInt64(&failed, 1)
					}
				}(i)
			}

			wg.Wait()
			elapsed := time.Since(start)

			fmt.Printf("\n=== %s ===\n", tt.name)
			fmt.Printf("maxConcurrent: %d\n", tt.maxConcurrent)
			fmt.Printf("Requests: %d | Sucessos: %d | Falhas: %d\n", tt.numRequests, success, failed)
			fmt.Printf("Tempo total: %v\n", elapsed)

			expectedMinTime := time.Duration(tt.numRequests) * tt.providerLatency / time.Duration(max(1, tt.maxConcurrent))
			fmt.Printf("Tempo mínimo esperado: ~%v\n", expectedMinTime)

			if success != int64(tt.numRequests) {
				t.Errorf("Esperado %d sucessos, got %d", tt.numRequests, success)
			}

			switch tt.maxConcurrent {
			case 0:
				if elapsed >= tt.providerLatency*time.Duration(tt.numRequests) {
					t.Errorf("Sem limitação deveria ser mais rápido que serial (%v >= %v)", elapsed, tt.providerLatency*time.Duration(tt.numRequests))
				}
			}
		})
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}