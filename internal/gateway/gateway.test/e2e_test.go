package gateway_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestE2EConcurrency(t *testing.T) {
	gatewayURL := "http://localhost:1313/v1/chat/completions"

	tests := []struct {
		name          string
		numRequests   int
		expectedMinMs int
		expectedMaxMs int
	}{
		{
			name:          "10 requests paralelas",
			numRequests:   10,
			expectedMinMs: 0,
			expectedMaxMs: 15000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wg sync.WaitGroup
			var success int64
			var failed int64
			var mu sync.Mutex
			results := make([]time.Duration, 0)

			start := time.Now()

			for i := 0; i < tt.numRequests; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()

					reqStart := time.Now()

					payload := map[string]interface{}{
						"model": "qwen-fast",
						"messages": []map[string]string{
							{"role": "user", "content": fmt.Sprintf("Responda apenas com o número: %d", id)},
						},
						"max_tokens": 10,
					}

					body, _ := json.Marshal(payload)
					resp, err := http.Post(gatewayURL, "application/json", bytes.NewReader(body))
					if err != nil {
						fmt.Printf("[E2E] [%d] ERRO: %v\n", id, err)
						atomic.AddInt64(&failed, 1)
						return
					}
					defer resp.Body.Close()

					respBody, _ := io.ReadAll(resp.Body)

					duration := time.Since(reqStart)

					mu.Lock()
					results = append(results, duration)
					mu.Unlock()

					if resp.StatusCode == 200 {
						atomic.AddInt64(&success, 1)
						fmt.Printf("[E2E] [%d] OK status=%d duration=%v\n", id, resp.StatusCode, duration)
					} else {
						atomic.AddInt64(&failed, 1)
						fmt.Printf("[E2E] [%d] FALHOU status=%d body=%s duration=%v\n", id, resp.StatusCode, string(respBody), duration)
					}
				}(i)
			}

			wg.Wait()
			totalDuration := time.Since(start)

			fmt.Printf("\n=== E2E: %s ===\n", tt.name)
			fmt.Printf("Total requests: %d\n", tt.numRequests)
			fmt.Printf("Sucessos: %d | Falhas: %d\n", success, failed)
			fmt.Printf("Tempo total: %v\n", totalDuration)

			var minDur, maxDur time.Duration
			for i, d := range results {
				if i == 0 || d < minDur {
					minDur = d
				}
				if d > maxDur {
					maxDur = d
				}
			}
			fmt.Printf("Menor duração: %v | Maior duração: %v\n", minDur, maxDur)

			if success != int64(tt.numRequests) {
				t.Errorf("Esperado %d sucessos, got %d", tt.numRequests, success)
			}
		})
	}
}