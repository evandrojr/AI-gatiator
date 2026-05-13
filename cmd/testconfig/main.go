package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/ai-gatiator/internal/gateway"
)

func main() {
	configPath := flag.String("c", "config.json", "Caminho para o arquivo de configuração")
	flag.Parse()

	data, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalf("Erro ao ler config: %v", err)
	}

	var cfg gateway.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("Erro ao parsear config: %v", err)
	}

	fmt.Printf("Server: MaxConcurrent=%d, TimeoutSec=%d\n", cfg.Server.MaxConcurrent, cfg.Server.TimeoutSec)
	fmt.Printf("Provider 0 api_keys_string: %q\n", cfg.Providers[0].APIKeysString)

	outBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		log.Fatalf("Erro ao serializar: %v", err)
	}
	fmt.Println(string(outBytes))
}