package gateway

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type ModelList struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func UpdateModels(cfgPath string, cfg *Config) {
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
		rawData, err := os.ReadFile(cfgPath)
		if err != nil {
			log.Fatalf("Erro ao ler config original: %v", err)
		}

		var rawCfg map[string]interface{}
		if err := json.Unmarshal(rawData, &rawCfg); err != nil {
			log.Fatalf("Erro ao parsear config original: %v", err)
		}

		providers, ok := rawCfg["providers"].([]interface{})
		if !ok {
			log.Fatalf("Config inválido: providers não encontrado")
		}

		for i, p := range cfg.Providers {
			if i < len(providers) {
				providerMap, ok := providers[i].(map[string]interface{})
				if ok {
					providerMap["models"] = p.Models
					if p.DefaultModel != "" {
						providerMap["default_model"] = p.DefaultModel
					}
					providers[i] = providerMap
				}
			}
		}
		rawCfg["providers"] = providers

		outBytes, err := json.MarshalIndent(rawCfg, "", "  ")
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

func (g *Gateway) HandleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var allModels []map[string]any
	seen := make(map[string]bool)

	st := g.getState()
	providers := st.availableProviders()
	for _, p := range providers {
		for _, m := range p.Models {
			if !seen[m] {
				seen[m] = true
				allModels = append(allModels, map[string]any{
					"id":      m,
					"object":  "model",
					"owned_by": p.Name,
				})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   allModels,
	})
}
