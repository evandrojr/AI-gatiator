@echo off



:: Este script chama o Claude Code redirecionando-o para o AI-gatiator local.
:: Ajusta o Base URL para apontar para o gateway
set ANTHROPIC_BASE_URL=http://127.0.0.1:1313
set ANTHROPIC_API_KEY=AI-gatiator-key

:: Executa o claude code repassando todos os argumentos (usando .cmd para evitar recursão)
@REM claude.cmd %*
C:\Users\Pichau\.local\bin\claude.exe %*