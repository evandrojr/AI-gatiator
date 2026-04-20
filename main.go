package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	loadEnv(".env")

	// Configuração do CLI usando o pacote padrão 'flag'
	configPath := flag.String("c", "config.json", "Caminho para o arquivo de configuração")
	updateModelsFlag := flag.Bool("update-models", false, "Conecta nas APIs e atualiza os modelos no config.json")
	installSvcFlag := flag.Bool("install-service", false, "Instala o AI-gatiator como um serviço do sistema (Linux/WSL)")

	// Customizando a mensagem do --help
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "🐱 AI-gatiator 🐾\n")
		fmt.Fprintf(os.Stderr, "Uso: %s [opções]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Opções disponíveis:\n")
		flag.PrintDefaults()
	}

	flag.Parse()

	// Tratamento de flags especiais
	if *installSvcFlag {
		installService()
		return
	}

	// Lê o arquivo de configuração
	data, err := os.ReadFile(*configPath)
	if err != nil {
		if os.IsNotExist(err) && *configPath == "config.json" {
			// Se for a primeira execução (config.json não existe), auto-gera um padrão
			log.Println("Arquivo config.json não encontrado. Gerando configuração padrão...")
			generateDefaultConfig(*configPath)
			return
		}
		log.Fatalf("Erro ao ler config: %v", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("Erro ao parsear config: %v", err)
	}

	if *updateModelsFlag {
		updateModels(*configPath, &cfg)
		return
	}

	// Valida provedores
	enabled := 0
	for _, p := range cfg.Providers {
		if p.Enabled {
			enabled++
			log.Printf("✔ Provedor carregado: %s (prioridade %d) modelo=%s", p.Name, p.Priority, p.DefaultModel)
		}
	}
	if enabled == 0 {
		log.Fatal("Nenhum provedor habilitado no config.json")
	}

	gw := NewGateway(cfg)
	go gw.WatchConfig(*configPath)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", gw.handleModels)
	mux.HandleFunc("/v1/", gw.handleGeneric)
	mux.HandleFunc("/health", gw.handleHealth)

	killProcessOnPort(cfg.Server.Port)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("🐱 AI-gatiator rodando em http://%s 🐾", addr)
	log.Printf("Endpoints: POST /v1/chat/completions | GET /v1/models | GET /health")

	if err := http.ListenAndServe(addr, loggingMiddleware(mux)); err != nil {
		log.Fatalf("Erro ao iniciar servidor: %v", err)
	}
}
