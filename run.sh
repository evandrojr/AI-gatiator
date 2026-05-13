#!/bin/bash

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

build() {
    echo "🔨 Build..."
    go build -ldflags="-s -w" -o AI-gatiator ./cmd/gateway/
    echo "✅ Build concluído: AI-gatiator"
}

case "${1:-run}" in
    build)
        build
        ;;
    run|"")
        build
        echo "🚀 Iniciando AI-gatiator..."
        ./AI-gatiator "${@:2}"
        ;;
    test)
        echo "🧪 Rodando testes..."
        go test ./...
        ;;
    test-v)
        echo "🧪 Rodando testes (verbose)..."
        go test -v ./...
        ;;
    *)
        build
        echo "🚀 Iniciando AI-gatiator..."
        ./AI-gatiator "$@"
        ;;
esac