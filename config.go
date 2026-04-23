package main

import (
	"encoding/json"
	"log"
	"os"
	"strings"
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
	APIKeys      []string          `json:"api_keys,omitempty"`
	APIKeysString string          `json:"api_keys_string,omitempty"` // vírgula-separated keys como alternativa ao array
	APIKey       string            `json:"api_key,omitempty"` // fallback para um único key
	DefaultModel string            `json:"default_model"`
	Models       []string          `json:"models"`
	Headers      map[string]string `json:"headers"`
}

func (p *ProviderConfig) GetAPIKeys() []string {
	if p.APIKeysString != "" {
		raw := os.ExpandEnv(p.APIKeysString)
		parts := strings.Split(raw, ",")
		var expanded []string
		for _, k := range parts {
			k = strings.TrimSpace(k)
			if k != "" {
				expanded = append(expanded, k)
			}
		}
		if len(expanded) > 0 {
			return expanded
		}
	}
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

// ─── Utils ────────────────────────────────────────────────────────────────────

func loadEnv(filename string, override bool) {
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
			if override || os.Getenv(key) == "" {
				os.Setenv(key, val)
			}
		}
	}
}

// ─── Auto-geração ─────────────────────────────────────────────────────────────

func generateDefaultConfig(path string) {
	defaultConfig := Config{
		Server: ServerConfig{
			Port:     1313,
			Host:     "0.0.0.0",
			LogLevel: "info",
		},
		Retry: RetryConfig{
			MaxAttempts: 3,
			DelayMs:     1000,
		},
		Providers: []ProviderConfig{
			{
				Name:         "openrouter",
				Enabled:      true,
				Priority:     1,
				BaseURL:      "https://openrouter.ai/api/v1",
				APIKeysString: "${OPENROUTER_KEYS}",
				DefaultModel: "google/gemini-2.5-flash-preview-04-17:free",
				Models: []string{
					"google/gemini-2.5-flash-preview-04-17:free",
					"google/gemini-2.0-flash-exp:free",
					"meta-llama/llama-3.3-70b-instruct:free",
					"anthropic/claude-3-haiku:free",
				},
				Headers: map[string]string{
					"HTTP-Referer": "http://localhost:1313",
					"X-Title":      "AI-gatiator",
				},
			},
			{
				Name:         "groq",
				Enabled:      false,
				Priority:     2,
				BaseURL:      "https://api.groq.com/openai/v1",
				APIKeysString: "${GROQ_KEYS}",
				DefaultModel: "llama-3.1-8b-instant",
				Models: []string{
					"llama-3.1-8b-instant",
					"llama-3.3-70b-versatile",
				},
				Headers: map[string]string{},
			},
			{
				Name:         "google",
				Enabled:      false,
				Priority:     3,
				BaseURL:      "https://generativelanguage.googleapis.com/v1beta/openai",
				APIKeysString: "${GOOGLE_KEYS}",
				DefaultModel: "gemini-2.0-flash",
				Models: []string{
					"gemini-2.0-flash",
					"gemini-2.5-flash-preview-04-17",
					"gemini-1.5-flash",
				},
				Headers: map[string]string{},
			},
		},
	}

	outBytes, err := json.MarshalIndent(defaultConfig, "", "  ")
	if err != nil {
		log.Fatalf("Erro ao gerar config.json padrão: %v", err)
	}
	if err := os.WriteFile(path, outBytes, 0644); err != nil {
		log.Fatalf("Erro ao salvar config.json padrão: %v", err)
	}

	// Criar o arquivo .env vazio caso não exista
	if _, err := os.Stat(".env"); os.IsNotExist(err) {
		envContent := `# Arquivo de variáveis de ambiente do AI-gatiator
# Múltiplas chaves separadas por vírgula:
OPENROUTER_KEYS=chave1,chave2,chave3
GROQ_KEYS=chave1,chave2
GOOGLE_KEYS=chave1,chave2
CEREBRAS_KEY=SUA_CHAVE_AQUI
SAMBANOVA_KEY=SUA_CHAVE_AQUI
DEEPSEEK_KEY=SUA_CHAVE_AQUI
OLLAMA_KEY=ollama
`
		os.WriteFile(".env", []byte(envContent), 0600)
	}

	log.Printf("✨ Arquivo de configuração padrão criado em %s. Configure suas chaves no arquivo .env e reinicie!", path)
	os.Exit(0)
}
