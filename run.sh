#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

kill_old() {
    EXEC_PATH="$(pwd)/AI-gatiator"

    if command -v lsof &> /dev/null; then
        PIDS=$(lsof -t -i:1313 2>/dev/null || true)
    elif command -v ss &> /dev/null; then
        PIDS=$(ss -lptn 'sport = :1313' 2>/dev/null | grep AI-gatiator | grep -o 'pid=[0-9]*' | cut -d= -f2 || true)
    fi

    for PID in $PIDS; do
        if [ -n "$PID" ] && [ "$PID" != "$$" ]; then
            CMD=$(ps -p $PID -o comm= 2>/dev/null || true)
            if [ "$CMD" == "AI-gatiator" ] || [ -f "/proc/$PID/exe" ]; then
                echo "🛑 Finalizando AI-gatiator antigo (PID: $PID)..."
                kill -9 $PID 2>/dev/null || true
            fi
        fi
    done

    if [ -n "$PIDS" ]; then
        sleep 1
    fi
}

build() {
    echo "🔨 Build..."
    go build -ldflags="-s -w" -o AI-gatiator ./cmd/gateway/
    echo "✅ Build concluído: AI-gatiator"
}

run() {
    kill_old
    echo "🚀 Iniciando AI-gatiator..."
    exec ./AI-gatiator "$@"
}

case "${1:-run}" in
    build)
        build
        ;;
    run|"")
        build
        run "${@:2}"
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
        run "$@"
        ;;
esac