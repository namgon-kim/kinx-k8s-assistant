package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	kubectlMCP "github.com/GoogleCloudPlatform/kubectl-ai/pkg/mcp"
)

func TestEnsureKinxMCPConfigUsesOnlyAppConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	appConfigDir := filepath.Join(home, ".k8s-assistant")
	if err := os.MkdirAll(appConfigDir, 0o755); err != nil {
		t.Fatalf("create app config dir: %v", err)
	}
	appConfig := filepath.Join(appConfigDir, "mcp.yaml")
	if err := os.WriteFile(appConfig, []byte(`servers:
  - name: trouble-shooting
    url: http://localhost:9091/mcp
    use_streaming: true
    timeout: 60
`), 0o644); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	kubectlConfig, err := kubectlMCP.DefaultConfigPath()
	if err != nil {
		t.Fatalf("get kubectl mcp path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(kubectlConfig), 0o755); err != nil {
		t.Fatalf("create kubectl config dir: %v", err)
	}
	if err := os.WriteFile(kubectlConfig, []byte(`servers:
  - name: log-analyzer
    url: http://localhost:9090/mcp
`), 0o644); err != nil {
		t.Fatalf("write stale kubectl config: %v", err)
	}

	cfg, path, err := ensureKinxMCPConfig()
	if err != nil {
		t.Fatalf("ensure MCP config: %v", err)
	}
	if path != appConfig {
		t.Fatalf("unexpected source path: got %s want %s", path, appConfig)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].Name != "trouble-shooting" {
		t.Fatalf("unexpected servers: %+v", cfg.Servers)
	}

	synced, err := os.ReadFile(kubectlConfig)
	if err != nil {
		t.Fatalf("read synced kubectl config: %v", err)
	}
	if strings.Contains(string(synced), "log-analyzer") {
		t.Fatalf("synced config should not include stale log-analyzer:\n%s", synced)
	}
	if !strings.Contains(string(synced), "trouble-shooting") {
		t.Fatalf("synced config should include app config server:\n%s", synced)
	}
}

func TestCheckKinxMCPServersOnlyChecksConfiguredHTTPServers(t *testing.T) {
	cfg := &mcpConfigFile{
		Servers: []mcpServerConfig{
			{Name: "log-analyzer", Command: "log-analyzer-server"},
			{Name: "trouble-shooting", URL: "http://127.0.0.1:1/mcp"},
		},
	}

	err := checkKinxMCPServers(cfg)
	if err == nil {
		t.Fatal("expected missing HTTP server error")
	}
	msg := err.Error()
	if strings.Contains(msg, "log-analyzer") {
		t.Fatalf("stdio server should not be preflight-checked: %s", msg)
	}
	if !strings.Contains(msg, "trouble-shooting") {
		t.Fatalf("configured HTTP server should be reported: %s", msg)
	}
}
