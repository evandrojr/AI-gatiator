#!/bin/bash
echo "Enviando pergunta de teste para o AI-gatiator..."
echo

curl -X POST http://localhost:1313/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer test" \
  -d '{
    "model": "",
    "messages": [{"role": "user", "content": "Olá, quem é você? Responda em uma frase curta."}],
    "stream": false
  }'

echo
echo
