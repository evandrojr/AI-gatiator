#!/bin/bash

# Este script chama o Claude Code redirecionando-o para o AI-gatiator local.
# Ajusta o Base URL para apontar para o gateway
export ANTHROPIC_BASE_URL="http://127.0.0.1:8080"
export ANTHROPIC_API_KEY="AI-gatiator-key"

# Executa o claude code repassando todos os argumentos
claude "$@"
