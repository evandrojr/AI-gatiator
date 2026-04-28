package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

// ─── Provider state (tracks failures) ────────────────────────────────────────

type ProviderState struct {
	mu                    sync.Mutex
	failures              int
	lastFailure           time.Time
	concurrencyBlocked    bool
	concurrencyBlockedAt  time.Time
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
	s.concurrencyBlocked = false
}

func (s *ProviderState) recordConcurrencyBlocked() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.concurrencyBlocked = true
	s.concurrencyBlockedAt = time.Now()
}

func (s *ProviderState) isConcurrencyBlocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.concurrencyBlocked {
		return false
	}
	if time.Since(s.concurrencyBlockedAt) > 5*time.Second {
		s.concurrencyBlocked = false
		return false
	}
	return true
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
			if st.States[stateKey].isAvailable() && !st.States[stateKey].isConcurrencyBlocked() {
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

const logFile = "/tmp/ai-gatiator-logs.json"

type LogEntry struct {
	Time         string `json:"time"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	DurationMs   int64  `json:"duration_ms"`
	StatusCode   int    `json:"status_code"`
	RequestBody  string `json:"request_body,omitempty"`
	ResponseBody string `json:"response_body,omitempty"`
}

type Gateway struct {
	mu         sync.RWMutex
	state      *GatewayState
	semaphores map[string]chan struct{}
	timeout    time.Duration
}

func readLogsFromFile() []LogEntry {
	data, err := os.ReadFile(logFile)
	if err != nil {
		return []LogEntry{}
	}
	var logs []LogEntry
	json.Unmarshal(data, &logs)
	return logs
}

func appendLogToFile(entry LogEntry) {
	logs := readLogsFromFile()
	logs = append(logs, entry)
	if len(logs) > 200 {
		logs = logs[len(logs)-200:]
	}
	data, _ := json.MarshalIndent(logs, "", "  ")
	os.WriteFile(logFile, data, 0644)
}

func NewGateway(cfg Config) *Gateway {
	s := &GatewayState{
		Config: cfg,
		States: make(map[string]*ProviderState),
	}

	timeoutSec := cfg.Server.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 60
	}
	for _, p := range cfg.Providers {
		if p.Enabled {
			for _, key := range p.GetAPIKeys() {
				stateKey := p.Name + ":" + key
				s.States[stateKey] = &ProviderState{}
			}
		}
	}

	semaphores := make(map[string]chan struct{})
	for _, p := range cfg.Providers {
		if p.Enabled {
			maxConc := p.MaxConcurrent
			if maxConc == 0 && cfg.Server.MaxConcurrent > 0 {
				maxConc = cfg.Server.MaxConcurrent
			}
			if maxConc > 0 {
				semaphores[p.Name] = make(chan struct{}, maxConc)
			}
		}
	}

	return &Gateway{state: s, semaphores: semaphores, timeout: time.Duration(timeoutSec) * time.Second}
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
			LoadEnv(".env", true) // Recarrega chaves de ambiente forçando sobrescrita

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

func (g *Gateway) Forward(reqMap map[string]any, initialModel string, w http.ResponseWriter, originalPath string) {
	st := g.getState()
	providers := st.availableProviders()
	log.Printf("[DEBUG] initialModel=%q available providers=%d", initialModel, len(providers))
	for i, p := range providers {
		log.Printf("[DEBUG] provider[%d]=%s priority=%d", i, p.Name, p.Priority)
	}
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
				log.Printf("[DEBUG] provider=%s supports model=%s, trying first", provider.Name, initialModel)
				for _, m := range provider.Models {
					if m != initialModel {
						modelsToTry = append(modelsToTry, m)
					}
				}
			} else {
				modelsToTry = append([]string{provider.DefaultModel}, provider.Models...)
				log.Printf("[DEBUG] provider=%s does NOT support %s, using default=%s", provider.Name, initialModel, provider.DefaultModel)
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

					// Sanitização para DeepSeek (Eva compat)
					if provider.Name == "deepseek" {
						g.sanitizeMessages(reqMap)
					}

					success, statusCode, body, blocked := g.tryProviderWithKey(provider, key, reqMap, w, originalPath)

					if blocked {
						log.Printf("[BLOCKED] %s concurrency limit reached, trying next provider", provider.Name)
						break // para este provider, tenta o próximo
					}

					if success {
						st.States[stateKey].recordSuccess()
						w.Header().Set("X-Gateway-Provider", provider.Name)
						w.Header().Set("X-Gateway-Model", model)
						return
					}

					// 404 = modelo não encontrado: tenta o próximo modelo do mesmo provider
					if statusCode == 404 {
						log.Printf("[SKIP] %s modelo=%s não encontrado (404), tentando próximo modelo...", provider.Name, model)
						continue
					}

					// 400 = formato incompatível com o provider (ex: tool_call_id ausente)
					// Afeta TODOS os modelos do mesmo provider — pula direto para o próximo provider
					if statusCode == 400 {
						log.Printf("[SKIP] %s formato incompatível (status=400) body=%s, pulando provider...", provider.Name, truncate(string(body), 200))
						break
					}

					st.States[stateKey].recordFailure()

					// 401/403 = erro de autenticação: não repassa ao cliente, apenas loga e tenta o próximo provider
					if statusCode == 401 || statusCode == 403 {
						log.Printf("[AUTH] %s (key=%s) chave inválida (status=%d), tentando próximo provider...", provider.Name, truncate(key, 8), statusCode)
						break // sai do loop de modelos, vai para a próxima chave — sem atualizar lastBody
					}

					log.Printf("[FAIL] %s (key=%s) modelo=%s tentativa=%d/%d status=%d", provider.Name, truncate(key, 8), model, attempt+1, maxAttempts, statusCode)
					lastStatusCode = statusCode
					lastBody = body

					// Rate Limit (429) ou Server Error (5xx): não adianta tentar outro modelo na MESMA chave
					if statusCode == 429 || statusCode >= 500 {
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

func (g *Gateway) tryProviderWithKey(p ProviderConfig, apiKey string, reqMap map[string]any, w http.ResponseWriter, originalPath string) (success bool, statusCode int, body []byte, semaphoreFull bool) {
	sem, hasSem := g.semaphores[p.Name]
	if hasSem {
		select {
		case sem <- struct{}{}:
		default:
			stateKey := p.Name + ":" + apiKey
			g.state.States[stateKey].recordConcurrencyBlocked()
			log.Printf("[BLOCKED] %s reached max_concurrent, skipping to next provider", p.Name)
			return false, 0, nil, true
		}
		defer func() { <-sem }()
	}

	reqBodyBytes, err := json.Marshal(reqMap)
	if err != nil {
		return false, 500, []byte(`{"error":"internal error encoding request"}`), false
	}

	path := strings.TrimPrefix(originalPath, "/v1")
	url := strings.TrimRight(p.BaseURL, "/") + path

	httpReq, err := http.NewRequest("POST", url, bytes.NewReader(reqBodyBytes))
	if err != nil {
		return false, 500, []byte(`{"error":"internal error creating request"}`), false
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	for k, v := range p.Headers {
		httpReq.Header.Set(k, v)
	}

	client := &http.Client{Timeout: g.timeout} // Aumentado para 60s

	isStream := false
	if streamVal, ok := reqMap["stream"].(bool); ok {
		isStream = streamVal
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return false, 502, []byte(fmt.Sprintf(`{"error":"erro ao conectar no provedor: %v"}`, err)), false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
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
		return true, resp.StatusCode, nil, false
	}

	errorBody, _ := io.ReadAll(resp.Body)
	return false, resp.StatusCode, errorBody, false
}

// ─── Handlers ─────────────────────────────────────────────────────────────────

func (g *Gateway) HandleGeneric(w http.ResponseWriter, r *http.Request) {
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

	g.Forward(reqMap, initialModel, w, r.URL.Path)
}

func (g *Gateway) HandleHealth(w http.ResponseWriter, r *http.Request) {
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

type statusRecorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

func (g *Gateway) LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		log.Printf("→ %s %s", r.Method, r.URL.Path)

		var reqSnippet string
		if r.Method == "POST" {
			body, err := io.ReadAll(r.Body)
			if err == nil {
				r.Body = io.NopCloser(bytes.NewReader(body))
				reqSnippet = truncate(string(body), 500)
			}
		}

		next.ServeHTTP(recorder, r)
		dur := time.Since(start)
		log.Printf("← %s %s %v", r.Method, r.URL.Path, dur)
		appendLogToFile(LogEntry{
			Time:         time.Now().Format(time.RFC3339),
			Method:       r.Method,
			Path:         r.URL.Path,
			DurationMs:   dur.Milliseconds(),
			StatusCode:   recorder.status,
			RequestBody:  reqSnippet,
			ResponseBody: truncate(recorder.body.String(), 1000),
		})
	})
}

func (g *Gateway) HandleLogs(w http.ResponseWriter, r *http.Request) {
	// Prefer systemd journal output for the service logs. If journalctl is unavailable
	// or fails (e.g., running on non-systemd systems), fall back to the internal log file.
	cmd := exec.Command("journalctl", "-u", "aigatiator", "--no-pager", "-n", "500", "--output=short-iso")
	out, err := cmd.Output()
	if err == nil {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(out)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	logs := readLogsFromFile()
	json.NewEncoder(w).Encode(map[string]any{
		"logs":    logs,
		"warning": fmt.Sprintf("failed to run journalctl: %v", err),
	})
}

func containsModel(models []string, target string) bool {
	log.Printf("[DEBUG] containsModel check: target=%q in models=%v", target, models)
	for _, m := range models {
		if m == target {
			log.Printf("[DEBUG] containsModel: FOUND match")
			return true
		}
	}
	log.Printf("[DEBUG] containsModel: NO match")
	return false
}

func (g *Gateway) sanitizeMessages(req map[string]any) {
	messages, ok := req["messages"].([]any)
	if !ok || len(messages) == 0 {
		return
	}

	var newMessages []any
	var lastToolCallID string
	
	log.Printf("[DEBUG] Sanitizing %d messages (Strict Order Mode)...", len(messages))

	for i, msgAny := range messages {
		msg, ok := msgAny.(map[string]any)
		if !ok {
			newMessages = append(newMessages, msgAny)
			continue
		}

		role, _ := msg["role"].(string)

		// 1. Normalização de Role (function -> tool)
		if role == "function" {
			msg["role"] = "tool"
			role = "tool"
		}

		// 2. Se for uma TOOL, precisamos garantir que a anterior foi um ASSISTANT com TOOL_CALLS
		if role == "tool" {
			id, _ := msg["tool_call_id"].(string)
			if id == "" {
				if lastToolCallID != "" {
					id = lastToolCallID
					msg["tool_call_id"] = id
				} else {
					id = fmt.Sprintf("call_gen_%d", i)
					msg["tool_call_id"] = id
				}
			}

			// Verifica se a última mensagem no novo histórico foi um assistant com este ID
			needAssistant := true
			if len(newMessages) > 0 {
				if lastMsg, ok := newMessages[len(newMessages)-1].(map[string]any); ok {
					lastRole, _ := lastMsg["role"].(string)
					if lastRole == "assistant" {
						if tcs, exists := lastMsg["tool_calls"].([]any); exists && len(tcs) > 0 {
							// Se já é um assistant com tool_calls, assumimos que está OK ou tentamos validar o ID
							needAssistant = false
						}
					}
				}
			}

			if needAssistant {
				log.Printf("[FIX] Injetando assistant fantasma antes da tool na posição %d", len(newMessages))
				ghostAssistant := map[string]any{
					"role": "assistant",
					"content": "",
					"tool_calls": []any{
						map[string]any{
							"id": id,
							"type": "function",
							"function": map[string]any{
								"name": "resolved_by_gateway",
								"arguments": "{}",
							},
						},
					},
				}
				newMessages = append(newMessages, ghostAssistant)
			}
			lastToolCallID = id
		}

		// 3. Se for ASSISTANT, capturamos o ID para a próxima TOOL
		if role == "assistant" {
			if toolCalls, exists := msg["tool_calls"].([]any); exists && len(toolCalls) > 0 {
				if firstCall, ok := toolCalls[0].(map[string]any); ok {
					if id, ok := firstCall["id"].(string); ok {
						lastToolCallID = id
					}
				}
			} else if fCall, exists := msg["function_call"].(map[string]any); exists {
				// Converte function_call legado para tool_calls
				lastToolCallID = fmt.Sprintf("call_conv_%d", time.Now().UnixNano())
				msg["tool_calls"] = []any{
					map[string]any{
						"id": lastToolCallID,
						"type": "function",
						"function": fCall,
					},
				}
				delete(msg, "function_call")
			}
		}

		// Limpeza de content null
		if content, exists := msg["content"]; exists && content == nil {
			msg["content"] = ""
		}

		newMessages = append(newMessages, msg)
	}

	req["messages"] = newMessages
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
