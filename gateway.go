package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// ─── Provider state (tracks failures) ────────────────────────────────────────

type ProviderState struct {
	mu           sync.Mutex
	failures     int
	lastFailure  time.Time
}

func (s *ProviderState) recordFailure() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	s.lastFailure = time.Now()
}

func (s *ProviderState) recordSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = 0
	s.lastFailure = time.Time{}
}

func (s *ProviderState) isAvailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.failures == 0 {
		return true
	}

	// Exponential backoff
	cooldown := time.Duration(30*(1<<(s.failures-1))) * time.Second
	if cooldown > 5*time.Minute {
		cooldown = 5 * time.Minute
	}

	return time.Since(s.lastFailure) > cooldown
}

// ─── Gateway ──────────────────────────────────────────────────────────────────

type GatewayState struct {
	Config Config
	States map[string]*ProviderState
}

func (st *GatewayState) availableProviders() []ProviderConfig {
	var active []ProviderConfig
	for _, p := range st.Config.Providers {
		if !p.Enabled {
			continue
		}
		hasAvailableKey := false
		for _, key := range p.GetAPIKeys() {
			stateKey := p.Name + ":" + key
			if st.States[stateKey].isAvailable() {
				hasAvailableKey = true
				break
			}
		}
		if hasAvailableKey {
			active = append(active, p)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].Priority < active[j].Priority
	})
	return active
}

type Gateway struct {
	mu    sync.RWMutex
	state *GatewayState
}

func NewGateway(cfg Config) *Gateway {
	s := &GatewayState{
		Config: cfg,
		States: make(map[string]*ProviderState),
	}
	for _, p := range cfg.Providers {
		if p.Enabled {
			for _, key := range p.GetAPIKeys() {
				stateKey := p.Name + ":" + key
				s.States[stateKey] = &ProviderState{}
			}
		}
	}
	return &Gateway{state: s}
}

func (g *Gateway) getState() *GatewayState {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.state
}

func (g *Gateway) WatchConfig(path string) {
	var lastModConfig time.Time
	var lastModEnv time.Time

	stat, err := os.Stat(path)
	if err == nil {
		lastModConfig = stat.ModTime()
	}
	statEnv, err := os.Stat(".env")
	if err == nil {
		lastModEnv = statEnv.ModTime()
	}

	for {
		time.Sleep(2 * time.Second)
		changed := false

		stat, err := os.Stat(path)
		if err == nil && stat.ModTime().After(lastModConfig) {
			lastModConfig = stat.ModTime()
			changed = true
		}

		statEnv, err := os.Stat(".env")
		if err == nil && statEnv.ModTime().After(lastModEnv) {
			lastModEnv = statEnv.ModTime()
			changed = true
		}

		if changed {
			loadEnv(".env", true) // Recarrega chaves de ambiente forçando sobrescrita

			data, err := os.ReadFile(path)
			if err != nil {
				log.Printf("Erro ao recarregar config (leitura): %v", err)
				continue
			}
			
			var newCfg Config
			if err := json.Unmarshal(data, &newCfg); err != nil {
				log.Printf("Erro ao parsear config recarregada: %v", err)
				continue
			}

			// Pega o estado antigo para copiar os states e não perder o backoff
			oldSt := g.getState()
			newSt := &GatewayState{
				Config: newCfg,
				States: make(map[string]*ProviderState),
			}

			for _, p := range newCfg.Providers {
				if p.Enabled {
					for _, key := range p.GetAPIKeys() {
						stateKey := p.Name + ":" + key
						if oldState, exists := oldSt.States[stateKey]; exists {
							newSt.States[stateKey] = oldState
						} else {
							newSt.States[stateKey] = &ProviderState{}
						}
					}
				}
			}

			g.mu.Lock()
			g.state = newSt
			g.mu.Unlock()

			log.Println("🔄 Configurações e ambiente recarregados com sucesso automaticamente!")
		}
	}
}

func (g *Gateway) forward(reqMap map[string]any, initialModel string, w http.ResponseWriter, originalPath string) {
	st := g.getState()
	providers := st.availableProviders()
	if len(providers) == 0 {
		http.Error(w, `{"error":"todos os provedores estão indisponíveis"}`, http.StatusServiceUnavailable)
		return
	}

	maxAttempts := st.Config.Retry.MaxAttempts
	delay := time.Duration(st.Config.Retry.DelayMs) * time.Millisecond

	var lastStatusCode int
	var lastBody []byte

	for attempt := 0; attempt < maxAttempts; attempt++ {
		for _, provider := range providers {
			for _, key := range provider.GetAPIKeys() {
				stateKey := provider.Name + ":" + key
				if !st.States[stateKey].isAvailable() {
					continue
				}

				var modelsToTry []string
				if initialModel != "" && containsModel(provider.Models, initialModel) {
					modelsToTry = append(modelsToTry, initialModel)
					for _, m := range provider.Models {
						if m != initialModel {
							modelsToTry = append(modelsToTry, m)
						}
					}
				} else {
					modelsToTry = append([]string{provider.DefaultModel}, provider.Models...)
				}

				// Filtra repetidos
				seen := make(map[string]bool)
				var filtered []string
				for _, m := range modelsToTry {
					if !seen[m] {
						seen[m] = true
						filtered = append(filtered, m)
					}
				}

				for _, model := range filtered {
					reqMap["model"] = model

					success, statusCode, body := g.tryProviderWithKey(provider, key, reqMap, w, originalPath)

					if success {
						st.States[stateKey].recordSuccess()
						w.Header().Set("X-Gateway-Provider", provider.Name)
						w.Header().Set("X-Gateway-Model", model)
						return
					}

					st.States[stateKey].recordFailure()
					log.Printf("[FAIL] %s (key=%s) modelo=%s tentativa=%d/%d status=%d", provider.Name, truncate(key, 8), model, attempt+1, maxAttempts, statusCode)
					
					lastStatusCode = statusCode
					lastBody = body

					// Se for Rate Limit (429), Auth Error (401, 403) ou Server Error (5xx), não adianta tentar outro modelo na MESMA chave
					if statusCode == 429 || statusCode == 401 || statusCode == 403 || statusCode >= 500 {
						break // sai do loop de modelos, vai para a próxima chave
					}
				}
			}
		}

		if attempt < maxAttempts-1 {
			time.Sleep(delay)
		}
	}

	if lastStatusCode > 0 && len(lastBody) > 0 {
		for k := range w.Header() {
			w.Header().Del(k)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(lastStatusCode)
		w.Write(lastBody)
		return
	}

	http.Error(w, `{"error":"todos os provedores e chaves falharam após múltiplas tentativas"}`, http.StatusBadGateway)
}

func (g *Gateway) tryProviderWithKey(p ProviderConfig, apiKey string, reqMap map[string]any, w http.ResponseWriter, originalPath string) (success bool, statusCode int, body []byte) {
	reqBodyBytes, err := json.Marshal(reqMap)
	if err != nil {
		return false, 500, []byte(`{"error":"internal error encoding request"}`)
	}

	path := strings.TrimPrefix(originalPath, "/v1")
	url := strings.TrimRight(p.BaseURL, "/") + path
	
	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(reqBodyBytes))
	if err != nil {
		return false, 500, []byte(`{"error":"internal error creating request"}`)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	for k, v := range p.Headers {
		httpReq.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 60 * time.Second} // Aumentado para 60s
	
	// Verifica se é stream
	isStream := false
	if streamVal, ok := reqMap["stream"].(bool); ok {
		isStream = streamVal
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return false, 502, []byte(fmt.Sprintf(`{"error":"erro ao conectar no provedor: %v"}`, err))
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// Proxy headers
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)

		if isStream {
			flusher, ok := w.(http.Flusher)
			buf := make([]byte, 4096)
			for {
				n, err := resp.Body.Read(buf)
				if n > 0 {
					w.Write(buf[:n])
					if ok {
						flusher.Flush()
					}
				}
				if err != nil {
					break
				}
			}
		} else {
			io.Copy(w, resp.Body)
		}
		return true, resp.StatusCode, nil
	}

	// Erro do provider
	errorBody, _ := io.ReadAll(resp.Body)
	return false, resp.StatusCode, errorBody
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

func (g *Gateway) handleGeneric(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
		return
	}

	var reqMap map[string]any
	if err := json.Unmarshal(body, &reqMap); err != nil {
		http.Error(w, `{"error":"invalid json"}`, http.StatusBadRequest)
		return
	}

	var initialModel string
	if m, ok := reqMap["model"].(string); ok {
		initialModel = m
	}

	g.forward(reqMap, initialModel, w, r.URL.Path)
}

func (g *Gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	status := make(map[string]any)

	st := g.getState()

	for _, p := range st.Config.Providers {
		if p.Enabled {
			provStatus := make(map[string]any)
			for _, key := range p.GetAPIKeys() {
				stateKey := p.Name + ":" + key
				s := st.States[stateKey]
				s.mu.Lock()
				provStatus[truncate(key, 8)] = map[string]any{
					"available":    s.failures == 0 || time.Since(s.lastFailure) > 5*time.Minute,
					"failures":     s.failures,
					"last_failure": s.lastFailure,
				}
				s.mu.Unlock()
			}
			status[p.Name] = provStatus
		}
	}

	json.NewEncoder(w).Encode(status)
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		log.Printf("→ %s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
		log.Printf("← %s %s %v", r.Method, r.URL.Path, time.Since(start))
	})
}

func containsModel(models []string, target string) bool {
	for _, m := range models {
		if m == target {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
