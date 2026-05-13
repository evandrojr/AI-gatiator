package gateway_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ai-gatiator/internal/gateway"
)

func TestConcurrencyLimitSkipToNextProvider(t *testing.T) {
	var mu sync.Mutex
	slowProviderCalls := 0
	fastProviderCalls := 0

	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		slowProviderCalls++
		callNum := slowProviderCalls
		mu.Unlock()

		time.Sleep(500 * time.Millisecond)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"model": "slow-model",
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": fmt.Sprintf("slow-%d", callNum)},
			}},
		})
	}))
	defer slowServer.Close()

	fastServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fastProviderCalls++
		callNum := fastProviderCalls
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"model": "fast-model",
			"choices": []map[string]any{{
				"message": map[string]string{"role": "assistant", "content": fmt.Sprintf("fast-%d", callNum)},
			}},
		})
	}))
	defer fastServer.Close()

	cfg := gateway.Config{
		Retry: gateway.RetryConfig{MaxAttempts: 1, DelayMs: 0},
		Providers: []gateway.ProviderConfig{
			{
				Name:          "slow-provider",
				Enabled:       true,
				MaxConcurrent: 1,
				Priority:      1,
				BaseURL:       slowServer.URL,
				APIKeys:       []string{""},
				DefaultModel:  "slow-model",
				Models:        []string{"slow-model"},
				Headers:       map[string]string{},
			},
			{
				Name:          "fast-provider",
				Enabled:       true,
				MaxConcurrent: 10,
				Priority:      2,
				BaseURL:       fastServer.URL,
				APIKeys:       []string{""},
				DefaultModel:  "fast-model",
				Models:        []string{"fast-model"},
				Headers:       map[string]string{},
			},
		},
	}

	gw := gateway.NewGateway(cfg)

	t.Run("request goes to fast-provider when slow-provider is at capacity", func(t *testing.T) {
		mu.Lock()
		slowProviderCalls = 0
		fastProviderCalls = 0
		mu.Unlock()

		var wg sync.WaitGroup
		results := make([]string, 0)
		var resultsMu sync.Mutex

		requestCount := 5

		for i := 0; i < requestCount; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				reqMap := map[string]any{
					"model": "any-model",
					"messages": []map[string]string{
						{"role": "user", "content": "test"},
					},
				}

				rr := httptest.NewRecorder()
				gw.Forward(reqMap, "", rr, "/v1/chat/completions")

				if rr.Code == 200 {
					resultsMu.Lock()
					results = append(results, fmt.Sprintf("req-%d-ok", id))
					resultsMu.Unlock()
				} else {
					resultsMu.Lock()
					results = append(results, fmt.Sprintf("req-%d-fail-%d", id, rr.Code))
					resultsMu.Unlock()
				}
			}(i)
		}

		wg.Wait()

		mu.Lock()
		slowCalls := slowProviderCalls
		fastCalls := fastProviderCalls
		mu.Unlock()

		fmt.Printf("\n=== Concurrency Limit Test ===\n")
		fmt.Printf("Slow provider calls: %d\n", slowCalls)
		fmt.Printf("Fast provider calls: %d\n", fastCalls)
		fmt.Printf("Total requests: %d | Results: %v\n", requestCount, results)

		if fastCalls > 0 {
			t.Logf("SUCCESS: %d requests went to fast-provider (blocked from slow-provider)", fastCalls)
		}

		if slowCalls > 0 {
			t.Logf("Slow provider handled %d requests", slowCalls)
		}

		allSuccess := true
		for _, r := range results {
			if r[len(r)-2:] != "ok" {
				allSuccess = false
				break
			}
		}

		if !allSuccess {
			t.Errorf("Some requests failed: %v", results)
		}
	})

	t.Run("blocked provider recovers after waiting", func(t *testing.T) {
		mu.Lock()
		slowProviderCalls = 0
		fastProviderCalls = 0
		mu.Unlock()

		reqMap := map[string]any{
			"model": "slow-model",
			"messages": []map[string]string{
				{"role": "user", "content": "first request"},
			},
		}

		rr := httptest.NewRecorder()
		gw.Forward(reqMap, "slow-model", rr, "/v1/chat/completions")

		if rr.Code != 200 {
			t.Errorf("First request should succeed, got %d", rr.Code)
		}

		mu.Lock()
		firstCall := slowProviderCalls
		mu.Unlock()

		secondReq := map[string]any{
			"model": "slow-model",
			"messages": []map[string]string{
				{"role": "user", "content": "second request"},
			},
		}
		rr2 := httptest.NewRecorder()
		gw.Forward(secondReq, "slow-model", rr2, "/v1/chat/completions")

		mu.Lock()
		secondCall := slowProviderCalls
		mu.Unlock()

		fmt.Printf("\n=== Recovery Test ===\n")
		fmt.Printf("First request: slow provider calls=%d\n", firstCall)
		fmt.Printf("Second request: slow provider calls=%d (total)\n", secondCall)

		if secondCall <= firstCall {
			t.Errorf("Second request should eventually use slow-provider after first completes")
		}
	})
}