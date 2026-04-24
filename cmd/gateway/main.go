package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/ai-gatiator/internal/gateway"
)

func main() {
	gateway.LoadEnv(".env", false)

	configPath := flag.String("c", "config.json", "Caminho para o arquivo de configuração")
	updateModelsFlag := flag.Bool("update-models", false, "Conecta nas APIs e atualiza os modelos no config.json")
	installSvcFlag := flag.Bool("install-service", false, "Instala o AI-gatiator como um serviço do sistema (Linux/WSL)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "AI-gatiator\n")
		fmt.Fprintf(os.Stderr, "Uso: %s [opções]\n\n", os.Args[0])
		flag.PrintDefaults()
	}

	flag.Parse()

	if *installSvcFlag {
		gateway.InstallService()
		return
	}

	data, err := os.ReadFile(*configPath)
	if err != nil {
		if os.IsNotExist(err) && *configPath == "config.json" {
			log.Println("Arquivo config.json não encontrado. Gerando configuração padrão...")
			gateway.GenerateDefaultConfig(*configPath)
			return
		}
		log.Fatalf("Erro ao ler config: %v", err)
	}

	var cfg gateway.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("Erro ao parsear config: %v", err)
	}

	// Set default timeout
	if cfg.Server.TimeoutSec <= 0 {
		cfg.Server.TimeoutSec = 60
	}

	if *updateModelsFlag {
		gateway.UpdateModels(*configPath, &cfg)
		return
	}

	enabled := 0
	for _, p := range cfg.Providers {
		if p.Enabled {
			enabled++
			log.Printf("Provider carregado: %s (prioridade %d) modelo=%s", p.Name, p.Priority, p.DefaultModel)
		}
	}
	if enabled == 0 {
		log.Fatal("Nenhum provedor habilitado no config.json")
	}

	gw := gateway.NewGateway(cfg)
	go gw.WatchConfig(*configPath)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", gw.HandleModels)
	mux.HandleFunc("/v1/", gw.HandleGeneric)
	mux.HandleFunc("/health", gw.HandleHealth)

	gateway.KillProcessOnPort(cfg.Server.Port)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	log.Printf("AI-gatiator rodando em http://%s", addr)
	log.Printf("Endpoints: POST /v1/chat/completions | GET /v1/models | GET /health")

	if err := http.ListenAndServe(addr, gateway.LoggingMiddleware(mux)); err != nil {
		log.Fatalf("Erro ao iniciar servidor: %v", err)
	}
}