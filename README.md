# 🐱 AI-gatiator 🐾

Servidor local em Go que expõe uma API OpenAI-compatible e faz fallback automático 🔄
entre múltiplos provedores de IA (OpenRouter, Groq, Google, Cerebras, SambaNova, DeepSeek, Ollama). 🐈

## ✨ Por que usar o AI-gatiator?

O grande superpoder deste projeto é permitir que você utilize ferramentas avançadas de IA (como o **Hermes Agent** e o **Claude Code**) **100% de graça!** 🤑

* **Burlar os Limites Gratuitos:** APIs gratuitas possuem cotas pequenas. Com o AI-gatiator, você pode cadastrar *várias chaves gratuitas* do mesmo provedor. Quando o limite (Rate Limit) da primeira chave estourar, o servidor passa automaticamente e de forma transparente para a próxima chave, e depois para o próximo modelo! O seu agente nunca fica na mão.
* **A Recomendação de Ouro (OpenRouter):** No momento, a recomendação oficial é focar no provedor **OpenRouter** (deixando os outros em `"enabled": false`). Motivos:
  1. **Tradução Universal:** Agentes como o *Claude Code* utilizam o formato da Anthropic (`/v1/messages`). APIs diretas como Groq ou Google não entendem isso e dão erro (404). O OpenRouter traduz do formato Anthropic para OpenAI debaixo dos panos!
  2. **Limites Gigantescos:** Agentes autônomos enviam *System Prompts* gigantescos (mais de 15.000 tokens por requisição). Provedores diretos como o Groq bloqueiam isso instantaneamente nas contas gratuitas (Erro 413). Já os modelos gratuitos do OpenRouter (ex: `google/gemini-2.5-flash`) suportam esses prompts gigantes sem suar!

## 🚀 Início rápido

### 1. Preencha as chaves no arquivo `.env` 🔑

Abra o arquivo `.env` (baseado no `.env.example`) e adicione as suas chaves de API.
Os provedores que você não possui chave podem ser desabilitados no `config.json` mudando para `"enabled": false`. 😸

| Provedor    | Onde obter a chave                          | Gratuito? |
|-------------|---------------------------------------------|-----------|
| OpenRouter  | https://openrouter.ai/keys                  | ✅ sim     |
| Groq        | https://console.groq.com/keys               | ✅ sim     |
| Google      | https://aistudio.google.com/apikey          | ✅ sim     |
| Cerebras    | https://cloud.cerebras.ai/                  | ✅ sim     |
| SambaNova   | https://cloud.sambanova.ai/                 | ✅ sim     |
| DeepSeek    | https://platform.deepseek.com/api_keys      | 💲 pago    |
| Ollama      | não precisa (local)                         | ✅ sim     |

### 2. Compile e rode

```bash
go build -o AI-gatiator .
./AI-gatiator
# ou com config em outro caminho:
./AI-gatiator /caminho/para/config.json
```

No Windows:
```cmd
go build -o AI-gatiator.exe .
AI-gatiator.exe
```

### 3. Aponte seu agente para o gateway 🎯

```
Base URL: http://localhost:1313/v1
API Key:  qualquer valor (ex: "gateway")
Modelo:   qualquer (o gateway substitui pelo modelo do provedor se necessário)
```

> [!WARNING]
> **Atenção usuários do Claude Code:** O Claude Code utiliza o formato de mensagens da Anthropic (`/v1/messages`). Como o AI-gatiator atua como um proxy transparente (não traduz payload), ele repassa a requisição exatamente neste formato. APIs puras como Google, Groq e Cerebras não entendem esse formato e retornarão **404**.
> 
> Para usar o Claude Code, você **deve habilitar o OpenRouter** no AI-gatiator (`"enabled": true`), pois os servidores do OpenRouter possuem um tradutor universal que converte o formato da Anthropic para o formato nativo de qualquer modelo gratuito (ex: `google/gemini-2.5-flash`, `groq/llama-3.3-70b-versatile`).

## 🛠️ Endpoints

| Método | Path                     | Descrição                        |
|--------|--------------------------|----------------------------------|
| POST   | /v1/chat/completions     | Completions com fallback         |
| GET    | /v1/models               | Lista todos os modelos           |
| GET    | /health                  | Status de cada provedor          |

## 🐈 Como o fallback funciona 🐾

1. Provedores são tentados em ordem de `priority` (menor = primeiro)
2. Se você configurou múltiplas chaves em um provedor usando `"api_keys": ["chave1", "chave2"]`, o gateway testará a **chave 1 com todos os modelos** configurados. Se falhar (ex: Rate Limit 429), testará a **chave 2 com todos os modelos**, e assim por diante.
3. Se todas as chaves e modelos de um provedor falharem, ele tenta o próximo provedor.
4. Após falha, a chave específica do provedor fica em cooldown progressivo (30s, 60s, 120s...)
5. Após sucesso, o contador de falhas daquela chave é zerado
6. Se o modelo solicitado não existir no provedor, usa o `default_model`

## 🧶 Ferramentas do Gatinho

### 🔄 Atualização Automática de Modelos

Você não precisa mais atualizar os modelos manualmente no seu `config.json`! O AI-gatiator possui um comando especial para conectar na API dos provedores ativos e atualizar os modelos:

```bash
./AI-gatiator --update-models
```
*Ele também é esperto: no OpenRouter, ele filtra e baixa apenas os modelos gratuitos! 😻*

### ⚙️ Instalação como Serviço (Linux / WSL)

Para que o AI-gatiator inicie automaticamente com o sistema e rode em segundo plano (background) de forma robusta, você pode instalá-arlo como um serviço do `systemd`:

```bash
sudo ./AI-gatiator --install-service
```
Isso criará o serviço, ativará o autostart no boot e já começará a rodar o gateway! 
Para ver os logs depois da instalação, use: `journalctl -u aigatiator -f`

## 📝 Exemplos de uso

### 🤖 Claude Code
O Claude Code pode ser facilmente configurado utilizando um script local ou variáveis de ambiente antes de iniciá-lo:
```bash
# Configura o Claude Code para usar o AI-gatiator localmente
export ANTHROPIC_BASE_URL="http://127.0.0.1:1313/v1/"
export ANTHROPIC_API_KEY="AI-gatiator"
claude
```

### 🧠 Hermes Agent
Para configurar o Hermes Agent, você deve editar o arquivo de configuração dele (ex: `config.yaml`). Adicione o seu modelo gratuito e aponte para o gateway conforme o exemplo abaixo:
```yaml
model:
  default: google/gemini-2.5-flash-preview-04-17:free
  provider: custom
  base_url: http://localhost:1313/v1
  context_length: 131072
providers: {}
fallback_providers: []
credential_pool_strategies: {}
```

### 💻 curl
```bash
curl http://localhost:1313/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer qualquer" \
  -d '{
    "model": "llama-3.3-70b-versatile",
    "messages": [{"role": "user", "content": "Olá!"}]
  }'
```

### 🐍 Python (OpenAI SDK)
```python
from openai import OpenAI

client = OpenAI(
    base_url="http://localhost:1313/v1",
    api_key="qualquer"
)

resp = client.chat.completions.create(
    model="llama-3.3-70b-versatile",
    messages=[{"role": "user", "content": "Olá!"}]
)
print(resp.choices[0].message.content)
```

### 🩺 Verificar saúde dos provedores
```bash
curl http://localhost:1313/health
```

## 💡 Dicas de Mestre 😼

- Habilite apenas os provedores que você tem chave: `"enabled": false`
- Para usar Ollama local: inicie o Ollama normalmente e habilite o provedor `ollama`
- O header `X-Gateway-Provider` na resposta indica qual provedor foi usado
- Streaming funciona automaticamente (o gateway faz proxy do stream) 😻
