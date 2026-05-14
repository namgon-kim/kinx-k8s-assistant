package toolconnector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/mcp"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/sandbox"
	"github.com/GoogleCloudPlatform/kubectl-ai/pkg/tools"
	"k8s.io/klog/v2"
)

type Registry struct {
	Tools      tools.Tools
	MCPManager *mcp.Manager
}

func NewRegistry(ctx context.Context, executor sandbox.Executor, enableMCP bool) (*Registry, error) {
	registry := &Registry{}
	registry.Tools.Init()
	registry.Tools.RegisterTool(tools.NewBashTool(executor))
	registry.Tools.RegisterTool(tools.NewKubectlTool(executor))
	registry.loadCustomTools(executor)

	if enableMCP {
		manager, err := RegisterMCPTools(ctx, &registry.Tools)
		if err != nil {
			return nil, err
		}
		registry.MCPManager = manager
	}

	return registry, nil
}

func (r *Registry) loadCustomTools(executor sandbox.Executor) {
	for _, path := range customToolConfigCandidates() {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := tools.LoadAndRegisterCustomTools(path); err != nil {
			klog.Warningf("custom tool 설정 로드 일부 실패 (%s): %v", path, err)
		}
		global := tools.Default()
		cloned := global.CloneWithExecutor(executor)
		for _, tool := range cloned.AllTools() {
			if r.Tools.Lookup(tool.Name()) != nil {
				continue
			}
			r.Tools.RegisterTool(tool)
		}
	}
}

func customToolConfigCandidates() []string {
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, ".config", "kubectl-ai", "tools.yaml"))
	}
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		candidates = append(candidates, filepath.Join(xdgConfig, "kubectl-ai", "tools.yaml"))
	}
	return candidates
}

func RegisterMCPTools(ctx context.Context, registry *tools.Tools) (*mcp.Manager, error) {
	manager, err := mcp.InitializeManager()
	if err != nil {
		return nil, err
	}

	if err := manager.RegisterWithToolSystem(ctx, func(serverName string, toolInfo mcp.Tool) error {
		schema, err := tools.ConvertToolToGollm(&toolInfo)
		if err != nil {
			return err
		}

		mcpTool := tools.NewMCPTool(serverName, toolInfo.Name, toolInfo.Description, schema, manager)
		schema.Name = mcpTool.UniqueToolName()
		schema.Description = fmt.Sprintf("%s (from %s)", toolInfo.Description, serverName)
		registry.RegisterTool(mcpTool)
		return nil
	}); err != nil {
		_ = manager.Close()
		return nil, err
	}

	return manager, nil
}

func (r *Registry) Close() error {
	if r == nil || r.MCPManager == nil {
		return nil
	}
	return r.MCPManager.Close()
}
