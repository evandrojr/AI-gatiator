package gateway

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func KillProcessOnPort(port int) {
	if runtime.GOOS == "windows" {
		portStr := fmt.Sprintf(":%d", port)
		cmd := exec.Command("netstat", "-ano")
		out, err := cmd.Output()
		if err != nil {
			return
		}

		lines := strings.Split(string(out), "\n")
		var pids []string
		for _, line := range lines {
			if strings.Contains(line, "LISTENING") && strings.Contains(line, portStr) {
				fields := strings.Fields(line)
				if len(fields) >= 5 {
					pids = append(pids, fields[len(fields)-1])
				}
			}
		}

		for _, pid := range pids {
			if pid != "0" {
				exec.Command("taskkill", "/F", "/PID", pid).Run()
				log.Printf("Processo antigo (PID %s) na porta %d finalizado.", pid, port)
			}
		}
	} else {
		// Linux / macOS
		portStr := fmt.Sprintf("%d", port)
		cmd := exec.Command("lsof", "-t", "-i", fmt.Sprintf(":%s", portStr))
		out, err := cmd.Output()
		pids := strings.Split(strings.TrimSpace(string(out)), "\n")

		if err != nil || len(pids) == 0 || pids[0] == "" {
			cmd = exec.Command("ss", "-lptn", fmt.Sprintf("sport = :%s", portStr))
			out, _ = cmd.Output()
			lines := strings.Split(string(out), "\n")
			for _, line := range lines {
				if strings.Contains(line, "AI-gatiator") && strings.Contains(line, "pid=") {
					parts := strings.Split(line, "pid=")
					if len(parts) > 1 {
						pidPart := strings.Split(parts[1], ",")[0]
						pids = append(pids, pidPart)
					}
				}
			}
		}

		for _, pid := range pids {
			pid = strings.TrimSpace(pid)
			if pid == "" {
				continue
			}
			commBytes, err := os.ReadFile(fmt.Sprintf("/proc/%s/comm", pid))
			if err == nil {
				name := strings.TrimSpace(string(commBytes))
				if strings.Contains(name, "AI-gatiator") {
					exec.Command("kill", "-9", pid).Run()
					log.Printf("Processo AI-gatiator antigo (PID %s) na porta %d finalizado.", pid, port)
				}
			}
		}
	}
}

func InstallService() {
	if runtime.GOOS != "linux" {
		log.Fatal("A instalação como serviço só é suportada no Linux.")
	}

	execPath, err := os.Executable()
	if err != nil {
		log.Fatalf("Erro ao obter caminho do executável: %v", err)
	}
	execPath, _ = filepath.Abs(execPath)
	workDir := filepath.Dir(execPath)

	username := os.Getenv("SUDO_USER")
	if username == "" {
		username = "root"
	}

	serviceContent := fmt.Sprintf(`[Unit]
Description=AI-gatiator Proxy Service
After=network.target

[Service]
Type=simple
User=%s
WorkingDirectory=%s
ExecStart=%s
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
`, username, workDir, execPath)

	servicePath := "/etc/systemd/system/aigatiator.service"
	if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
		if os.IsPermission(err) {
			log.Fatal("❌ Permissão negada! A instalação requer privilégios de administrador. Rode:\n   sudo ./AI-gatiator --install-service")
		}
		log.Fatalf("Erro ao escrever arquivo de serviço em %s: %v", servicePath, err)
	}

	fmt.Printf("✔ Arquivo de serviço criado em %s\n", servicePath)

	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		log.Fatalf("Erro ao recarregar systemd: %v", err)
	}
	if err := exec.Command("systemctl", "enable", "aigatiator").Run(); err != nil {
		log.Fatalf("Erro ao habilitar serviço: %v", err)
	}
	if err := exec.Command("systemctl", "start", "aigatiator").Run(); err != nil {
		log.Fatalf("Erro ao iniciar serviço: %v", err)
	}

	fmt.Println("🚀 Serviço AI-gatiator instalado e iniciado com sucesso!")
	fmt.Println("Para ver os logs, digite: journalctl -u aigatiator -f")
	fmt.Println("Para parar o serviço, digite: sudo systemctl stop aigatiator")
}
