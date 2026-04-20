#!/bin/bash

# Script para automatizar a criação de releases no GitHub
# Isso irá acionar a GitHub Action que compila e publica os binários automaticamente!

echo "🐱 AI-gatiator Release Automator 🐾"
echo "-----------------------------------"

VERSION=$1

# Se a versão não foi passada via parâmetro, pergunta ao usuário
if [ -z "$VERSION" ]; then
    read -p "Digite o número da versão para a release (ex: 1.0.1): " VERSION
fi

# Adiciona o prefixo 'v' caso o usuário tenha esquecido (ex: 1.0.1 vira v1.0.1)
if [[ $VERSION != v* ]]; then
    VERSION="v$VERSION"
fi

echo ""
echo "🚀 Preparando para lançar a versão $VERSION..."

# Verifica se há alterações não commitadas
if ! git diff-index --quiet HEAD --; then
    echo "⚠️ Você possui alterações pendentes no seu código que ainda não foram salvas no Git."
    read -p "Deseja adicionar (git add) e commitar (git commit) essas alterações automaticamente agora? (s/N): " COMMIT_ANS
    if [[ "$COMMIT_ANS" == "s" || "$COMMIT_ANS" == "S" ]]; then
        git add .
        git commit -m "chore: release $VERSION"
        echo "✔ Alterações commitadas."
    else
        echo "❌ Lançamento abortado. Por favor, faça o commit das suas alterações manualmente primeiro."
        exit 1
    fi
fi

# Cria a nova Tag
echo "📦 Criando a tag $VERSION..."
# Deleta a tag localmente caso já exista e estejamos tentando forçar
git tag -d $VERSION 2>/dev/null
git tag -a $VERSION -m "Release $VERSION"

# Pega o nome do branch atual
BRANCH=$(git rev-parse --abbrev-ref HEAD)

echo "☁️ Enviando para o GitHub..."
git push origin $BRANCH
git push origin $VERSION

# Calcula a URL do repositório para facilitar o clique
REPO_URL=$(git config --get remote.origin.url)
REPO_PATH=$(echo $REPO_URL | sed 's/.*github.com[:\/]//;s/\.git$//')

echo ""
echo "✅ Sucesso Mundial Alcançado!"
echo "A tag $VERSION foi enviada para o servidor."
echo "Os robozinhos da GitHub Action já começaram a rodar para compilar os executáveis para Windows, Linux e Mac!"
echo ""
echo "👉 Acompanhe o progresso do build aqui: https://github.com/$REPO_PATH/actions"
echo "📦 Sua release aparecerá em breve aqui: https://github.com/$REPO_PATH/releases"
