package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// ─── Config structs ───────────────────────────────────────────────────────────

type ServerConfig struct {
	Port     int    `json:"port"`
	Host     string `json:"host"`
	LogLevel string `json:"log_level"`
}

type RetryConfig struct {
	MaxAttempts int `json:"max_attempts"`
	DelayMs     int `json:"delay_ms"`
}

type ProviderConfig struct {
	Name         string            `json:"name"`
	Enabled      bool              `json:"enabled"`
	Priority     int               `json:"priority"`
	BaseURL      string            `json:"base_url"`
	APIKey       string            `json:"api_key"`
	APIKeys      []string          `json:"api_keys"`
	DefaultModel string            `json:"default_model"`
	Models       []string          `json:"models"`
	Headers      map[string]string `json:"headers"`
}

func (p *ProviderConfig) GetAPIKeys() []string {
	if len(p.APIKeys) > 0 {
		var expanded []string
		for _, k := range p.APIKeys {
			expanded = append(expanded, os.ExpandEnv(k))
		}
		return expanded
	}
	if p.APIKey != "" {
		return []string{os.ExpandEnv(p.APIKey)}
	}
	return []string{""}
}

type Config struct {
	Server    ServerConfig     `json:"server"`
	Retry     RetryConfig      `json:"retry"`
	Providers []ProviderConfig `json:"providers"`
}

// ─── Provider state (tracks failures) ────────────────────────────────────────

type ProviderState struct {
	mu          sync.Mutex
	failures    int
	lastFailure time.Time
	cooldown    time.Duration
}

func (ps *ProviderState) recordFailure() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.failures++
	ps.lastFailure = time.Now()
	// Backoff progressivo: 30s, 60s, 120s ...
	ps.cooldown = time.Duration(30*(1<<min(ps.failures-1, 3))) * time.Second
}

func (ps *ProviderState) recordSuccess() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.failures = 0
	ps.cooldown = 0
}

func (ps *ProviderState) isAvailable() bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.failures == 0 {
		return true
	}
	return time.Since(ps.lastFailure) > ps.cooldown
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ─── Gateway ──────────────────────────────────────────────────────────────────

type Gateway struct {
	config  Config
	states  map[string]*ProviderState
	client  *http.Client
}

func NewGateway(cfg Config) *Gateway {
	states := make(map[string]*ProviderState)
	for _, p := range cfg.Providers {
		for _, key := range p.GetAPIKeys() {
			stateKey := p.Name + ":" + key
			states[stateKey] = &ProviderState{}
		}
	}
	return &Gateway{
		config: cfg,
		states: states,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// Retorna provedores habilitados e disponíveis, ordenados por prioridade
func (g *Gateway) availableProviders() []ProviderConfig {
	var list []ProviderConfig
	for _, p := range g.config.Providers {
		if !p.Enabled {
			continue
		}
		
		isAvail := false
		for _, key := range p.GetAPIKeys() {
			if g.states[p.Name+":"+key].isAvailable() {
				isAvail = true
				break
			}
		}

		if isAvail {
			list = append(list, p)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Priority < list[j].Priority
	})
	return list
}

// O Gateway foi refatorado para utilizar map[string]any genérico e suportar qualquer formato de API


// ─── Lógica de fallback ───────────────────────────────────────────────────────

func (g *Gateway) forward(reqMap map[string]any, initialModel string, w http.ResponseWriter, originalPath string) {
	providers := g.availableProviders()
	if len(providers) == 0 {
		http.Error(w, `{"error":"todos os provedores estão indisponíveis"}`, http.StatusServiceUnavailable)
		return
	}

	maxAttempts := g.config.Retry.MaxAttempts
	delay := time.Duration(g.config.Retry.DelayMs) * time.Millisecond

	var lastStatusCode int
	var lastBody []byte

	for attempt := 0; attempt < maxAttempts; attempt++ {
		for _, provider := range providers {
			for _, key := range provider.GetAPIKeys() {
				stateKey := provider.Name + ":" + key
				if !g.states[stateKey].isAvailable() {
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
					if len(provider.Models) > 0 {
						modelsToTry = provider.Models
					} else {
						modelsToTry = []string{provider.DefaultModel}
					}
				}

				for _, model := range modelsToTry {
					reqMap["model"] = model
					success, statusCode, body := g.tryProviderWithKey(provider, key, reqMap, w, originalPath)

					if success {
						g.states[stateKey].recordSuccess()
						log.Printf("[OK] %s (key=%s) → modelo=%s", provider.Name, truncate(key, 8), model)
						return
					}

					g.states[stateKey].recordFailure()
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

	resp, err := g.client.Do(httpReq)
	if err != nil {
		log.Printf("[ERR] %s: %v", p.Name, err)
		return false, 502, []byte(`{"error":"bad gateway: ` + err.Error() + `"}`)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Printf("[ERR] %s status=%d body=%s", p.Name, resp.StatusCode, truncate(string(bodyBytes), 200))
		return false, resp.StatusCode, bodyBytes
	}

	// Sucesso: streaming ou normal
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	// Garante header de provedor para debug
	w.Header().Set("X-Gateway-Provider", p.Name)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
	return true, resp.StatusCode, nil
}

// ─── Handlers HTTP ────────────────────────────────────────────────────────────

func (g *Gateway) handleGeneric(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "método não permitido", http.StatusMethodNotAllowed)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "erro ao ler body", http.StatusBadRequest)
		return
	}

	dec := json.NewDecoder(bytes.NewReader(bodyBytes))
	dec.UseNumber()
	var reqMap map[string]any
	if err := dec.Decode(&reqMap); err != nil {
		http.Error(w, "JSON inválido", http.StatusBadRequest)
		return
	}

	var model string
	if m, ok := reqMap["model"].(string); ok {
		model = m
	}

	g.forward(reqMap, model, w, r.URL.Path)
}

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	type ModelEntry struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
	}
	type ModelsResponse struct {
		Object string       `json:"object"`
		Data   []ModelEntry `json:"data"`
	}

	var models []ModelEntry
	seen := map[string]bool{}
	for _, p := range g.config.Providers {
		if !p.Enabled {
			continue
		}
		for _, m := range p.Models {
			key := p.Name + "/" + m
			if !seen[key] {
				seen[key] = true
				models = append(models, ModelEntry{
					ID:      m,
					Object:  "model",
					OwnedBy: p.Name,
				})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ModelsResponse{Object: "list", Data: models})
}

func (g *Gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	type ProviderStatus struct {
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		Available bool   `json:"available"`
		Failures  int    `json:"failures"`
	}
	type HealthResponse struct {
		Status    string           `json:"status"`
		Providers []ProviderStatus `json:"providers"`
	}

	var statuses []ProviderStatus
	for _, p := range g.config.Providers {
		isAvailable := false
		totalFailures := 0
		
		for _, key := range p.GetAPIKeys() {
			state := g.states[p.Name+":"+key]
			state.mu.Lock()
			failures := state.failures
			state.mu.Unlock()
			
			totalFailures += failures
			
			// Check availability without lock (since we already extracted what we need, or just call isAvailable directly)
			if state.isAvailable() {
				isAvailable = true
			}
		}

		statuses = append(statuses, ProviderStatus{
			Name:      p.Name,
			Enabled:   p.Enabled,
			Available: p.Enabled && isAvailable,
			Failures:  totalFailures,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(HealthResponse{Status: "ok", Providers: statuses})
}

// ─── Middleware de log ────────────────────────────────────────────────────────

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		log.Printf("→ %s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
		log.Printf("← %s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

// ─── Utilities ──────────────────────────────────────────────────────────────

func containsModel(models []string, model string) bool {
	for _, m := range models {
		if m == model {
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

func loadEnv(filename string) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return // ignorar se não existir
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			if os.Getenv(key) == "" {
				os.Setenv(key, val)
			}
		}
	}
}

// ─── Main ─────────────────────────────────────────────────────────────────────

func main() {
	loadEnv(".env")

	cfgPath := "config.json"
	if len(os.Args) > 1 {
		cfgPath = os.Args[1]
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		log.Fatalf("Erro ao ler config: %v", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("Erro ao parsear config: %v", err)
	}

	if len(os.Args) > 1 && os.Args[1] == "--update-models" {
		updateModels(cfgPath, &cfg)
		return
	}
	if len(os.Args) > 2 && os.Args[2] == "--update-models" {
		updateModels(cfgPath, &cfg)
		return
	}

	// Valida provedores
	enabled := 0
	for _, p := range cfg.Providers {
		if p.Enabled {
			enabled++
			log.Printf("Provedor carregado: %s (prioridade %d) modelo=%s", p.Name, p.Priority, p.DefaultModel)
		}
	}
	if enabled == 0 {
		log.Fatal("Nenhum provedor habilitado no config.json")
	}

	gw := NewGateway(cfg)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", gw.handleModels)
	mux.HandleFunc("/v1/", gw.handleGeneric)
	mux.HandleFunc("/health", gw.handleHealth)

	killProcessOnPort(cfg.Server.Port)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("AI-gatiator rodando em http://%s", addr)
	log.Printf("Endpoints: POST /v1/chat/completions | GET /v1/models | GET /health")

	if err := http.ListenAndServe(addr, loggingMiddleware(mux)); err != nil {
		log.Fatalf("Erro ao iniciar servidor: %v", err)
	}
}

// ─── Encerramento de processo pré-existente ───────────────────────────────────

func killProcessOnPort(port int) {
	portStr := fmt.Sprintf("%d", port)

	if runtime.GOOS == "windows" {
		cmd := exec.Command("cmd", "/c", fmt.Sprintf("netstat -ano | findstr LISTENING | findstr :%s", portStr))
		out, _ := cmd.Output()
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			if strings.Contains(line, fmt.Sprintf(":%s", portStr)) {
				fields := strings.Fields(line)
				if len(fields) >= 5 {
					pid := fields[len(fields)-1]
					cmdTL := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %s", pid), "/FO", "CSV")
					outTL, _ := cmdTL.Output()
					if strings.Contains(string(outTL), "AI-gatiator") {
						exec.Command("taskkill", "/F", "/PID", pid).Run()
						log.Printf("Processo AI-gatiator antigo (PID %s) na porta %d finalizado.", pid, port)
					}
				}
			}
		}
	} else if runtime.GOOS == "linux" {
		// Tenta lsof primeiro
		cmd := exec.Command("lsof", "-t", "-i", fmt.Sprintf("tcp:%s", portStr))
		out, _ := cmd.Output()
		pids := strings.Fields(string(out))

		if len(pids) == 0 {
			// Tenta ss como fallback
			cmd = exec.Command("ss", "-lptn", fmt.Sprintf("sport = :%s", portStr))
			out, _ = cmd.Output()
			lines := strings.Split(string(out), "\n")
			for _, line := range lines {
				if strings.Contains(line, "AI-gatiator") && strings.Contains(line, "pid=") {
					parts := strings.Split(line, "pid=")
					if len(parts) > 1 {
						pidPart := strings.Split(parts[1], ",")[0]
						pids = append(pids, pidPart)
					}
				}
			}
		}

		for _, pid := range pids {
			pid = strings.TrimSpace(pid)
			if pid == "" {
				continue
			}
			commBytes, err := os.ReadFile(fmt.Sprintf("/proc/%s/comm", pid))
			if err == nil {
				name := strings.TrimSpace(string(commBytes))
				if strings.Contains(name, "AI-gatiator") {
					exec.Command("kill", "-9", pid).Run()
					log.Printf("Processo AI-gatiator antigo (PID %s) na porta %d finalizado.", pid, port)
				}
			}
		}
	}
}

type ModelList struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func updateModels(cfgPath string, cfg *Config) {
	log.Println("Iniciando atualização automática de modelos...")
	updated := false

	for i, p := range cfg.Providers {
		if !p.Enabled {
			continue
		}

		baseURL := strings.TrimRight(p.BaseURL, "/")
		modelsURL := ""
		if strings.HasSuffix(baseURL, "/chat/completions") {
			modelsURL = strings.TrimSuffix(baseURL, "/chat/completions") + "/models"
		} else {
			modelsURL = baseURL + "/models"
		}

		req, err := http.NewRequest("GET", modelsURL, nil)
		if err != nil {
			log.Printf("Erro ao criar request para %s: %v", p.Name, err)
			continue
		}

		keys := p.GetAPIKeys()
		if len(keys) > 0 && keys[0] != "" {
			req.Header.Set("Authorization", "Bearer "+keys[0])
		}

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("Erro ao consultar %s: %v", p.Name, err)
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			log.Printf("Erro %d do provedor %s ao buscar modelos. Resposta: %s", resp.StatusCode, p.Name, string(body))
			continue
		}

		var ml ModelList
		if err := json.Unmarshal(body, &ml); err != nil {
			log.Printf("Erro ao parsear resposta de %s: %v", p.Name, err)
			continue
		}

		var newModels []string
		for _, m := range ml.Data {
			if p.Name == "openrouter" {
				if strings.Contains(strings.ToLower(m.ID), "free") {
					newModels = append(newModels, m.ID)
				}
			} else {
				newModels = append(newModels, m.ID)
			}
		}

		if len(newModels) > 0 {
			cfg.Providers[i].Models = newModels
			hasDefault := false
			for _, m := range newModels {
				if m == cfg.Providers[i].DefaultModel {
					hasDefault = true
					break
				}
			}
			if !hasDefault {
				cfg.Providers[i].DefaultModel = newModels[0]
			}
			log.Printf("✔ Provedor '%s' atualizado: %d modelos encontrados.", p.Name, len(newModels))
			updated = true
		} else {
			log.Printf("❌ Provedor '%s' não retornou nenhum modelo válido.", p.Name)
		}
	}

	if updated {
		outBytes, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			log.Fatalf("Erro ao serializar config atualizado: %v", err)
		}
		if err := os.WriteFile(cfgPath, outBytes, 0644); err != nil {
			log.Fatalf("Erro ao salvar %s: %v", cfgPath, err)
		}
		log.Println("✨ Atualização concluída! O arquivo config.json foi salvo com sucesso.")
	} else {
		log.Println("Nenhum modelo novo precisou ser salvo.")
	}
}
