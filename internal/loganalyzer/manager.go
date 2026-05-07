package loganalyzer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const defaultPort = 9090

// Manager는 log-analyzer-server 프로세스를 관리합니다.
type Manager struct {
	port    int
	pidFile string
	cmd     *exec.Cmd
}

// NewManager는 새 Manager를 생성합니다.
func NewManager(appDir string) *Manager {
	pidFile := ""
	if appDir != "" {
		pidFile = filepath.Join(appDir, "log-analyzer.pid")
	}
	return &Manager{
		port:    defaultPort,
		pidFile: pidFile,
	}
}

// Start는 log-analyzer-server를 시작합니다.
// 이미 실행 중이면 아무것도 하지 않습니다.
func (m *Manager) Start() error {
	// 이미 실행 중인지 확인
	if m.isRunning() {
		return nil
	}

	// log-analyzer-server 바이너리 찾기
	binPath := m.findBinary()
	if binPath == "" {
		return fmt.Errorf("log-analyzer-server 바이너리를 찾을 수 없음")
	}

	// 프로세스 시작
	m.cmd = exec.Command(binPath, "--port", strconv.Itoa(m.port))
	m.cmd.Stdout = nil
	m.cmd.Stderr = nil
	m.cmd.SysProcAttr = nil

	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("log-analyzer-server 시작 실패: %w", err)
	}

	// PID 저장
	if m.pidFile != "" {
		_ = os.WriteFile(m.pidFile, []byte(strconv.Itoa(m.cmd.Process.Pid)), 0o644)
	}

	// 서버가 시작될 때까지 대기
	time.Sleep(500 * time.Millisecond)

	return nil
}

// Stop은 log-analyzer-server를 종료합니다.
func (m *Manager) Stop() error {
	if m.cmd == nil || m.cmd.Process == nil {
		return nil
	}
	return m.cmd.Process.Kill()
}

// isRunning은 log-analyzer-server가 실행 중인지 확인합니다.
func (m *Manager) isRunning() bool {
	if m.pidFile == "" {
		return false
	}

	data, err := os.ReadFile(m.pidFile)
	if err != nil {
		return false
	}

	pid, err := strconv.Atoi(string(data))
	if err != nil {
		return false
	}

	// 프로세스 확인
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = proc.Signal(os.Signal(nil))
	return err == nil
}

// findBinary는 log-analyzer-server 바이너리 경로를 찾습니다.
func (m *Manager) findBinary() string {
	candidates := []string{
		"log-analyzer-server",
		"./bin/log-analyzer-server",
		filepath.Join(os.Getenv("HOME"), ".k8s-assistant", "bin", "log-analyzer-server"),
	}

	for _, path := range candidates {
		if _, err := exec.LookPath(path); err == nil {
			return path
		}
	}
	return ""
}

// Port는 log-analyzer-server의 포트를 반환합니다.
func (m *Manager) Port() int {
	return m.port
}
