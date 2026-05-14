package toolconnector

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	kubectlMCP "github.com/GoogleCloudPlatform/kubectl-ai/pkg/mcp"
	"gopkg.in/yaml.v3"
)

type mcpConfigFile struct {
	Servers []mcpServerConfig `yaml:"servers"`
}

type mcpServerConfig struct {
	Name         string            `yaml:"name"`
	Command      string            `yaml:"command,omitempty"`
	Args         []string          `yaml:"args,omitempty"`
	Env          map[string]string `yaml:"env,omitempty"`
	URL          string            `yaml:"url,omitempty"`
	UseStreaming bool              `yaml:"use_streaming,omitempty"`
	Timeout      int               `yaml:"timeout,omitempty"`
	SkipVerify   bool              `yaml:"skip_verify,omitempty"`
	Auth         map[string]any    `yaml:"auth,omitempty"`
	OAuth        map[string]any    `yaml:"oauth,omitempty"`
}

func PrepareKinxMCPClient() (string, error) {
	cfg, path, err := ensureKinxMCPConfig()
	if err != nil {
		return "", err
	}
	if err := checkKinxMCPServers(cfg); err != nil {
		return path, err
	}
	return path, nil
}

func ensureKinxMCPConfig() (*mcpConfigFile, string, error) {
	appPath, err := defaultKinxMCPConfigPath()
	if err != nil {
		return nil, "", err
	}
	cfg := mcpConfigFile{}

	data, err := os.ReadFile(appPath)
	if os.IsNotExist(err) {
		return nil, "", fmt.Errorf("MCP 설정 파일이 없습니다: %s", appPath)
	}
	if err != nil {
		return nil, "", fmt.Errorf("read MCP config %s: %w", appPath, err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, "", fmt.Errorf("parse MCP config %s: %w", appPath, err)
	}

	if len(cfg.Servers) == 0 {
		return nil, "", fmt.Errorf("MCP 서버가 설정되지 않았습니다: %s", appPath)
	}

	kubectlPath, err := kubectlMCP.DefaultConfigPath()
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(kubectlPath), 0o755); err != nil {
		return nil, "", fmt.Errorf("create MCP config dir: %w", err)
	}
	data, err = yaml.Marshal(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("marshal MCP config: %w", err)
	}
	if err := os.WriteFile(kubectlPath, data, 0o644); err != nil {
		return nil, "", fmt.Errorf("write MCP config %s: %w", kubectlPath, err)
	}
	return &cfg, appPath, nil
}

func defaultKinxMCPConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("getting user home directory: %w", err)
	}
	return filepath.Join(home, ".k8s-assistant", "mcp.yaml"), nil
}

func checkKinxMCPServers(cfg *mcpConfigFile) error {
	var missing []string
	for _, server := range cfg.Servers {
		if strings.TrimSpace(server.URL) == "" {
			continue
		}
		addr, ok := mcpServerAddress(server.URL)
		if !ok {
			continue
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			missing = append(missing, fmt.Sprintf("- %s (%s)", server.Name, addr))
			continue
		}
		_ = conn.Close()
	}
	if len(missing) > 0 {
		return fmt.Errorf("MCP 서버가 실행 중이 아닙니다.\n먼저 별도 터미널에서 실행하세요:\n%s", strings.Join(missing, "\n"))
	}
	return nil
}

func mcpServerAddress(rawURL string) (string, bool) {
	rawURL = strings.TrimSpace(rawURL)
	isHTTPS := strings.HasPrefix(rawURL, "https://")
	withoutScheme := strings.TrimPrefix(rawURL, "http://")
	withoutScheme = strings.TrimPrefix(withoutScheme, "https://")
	hostPort := strings.Split(withoutScheme, "/")[0]
	host, port, err := net.SplitHostPort(hostPort)
	if err == nil {
		return net.JoinHostPort(host, port), true
	}
	if hostPort == "" {
		return "", false
	}
	if strings.Contains(hostPort, ":") {
		return hostPort, true
	}
	port = "80"
	if isHTTPS {
		port = "443"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", false
	}
	return net.JoinHostPort(hostPort, port), true
}
