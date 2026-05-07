package k8s

import (
	"fmt"
	"sort"

	"k8s.io/client-go/tools/clientcmd"
)

// KubeconfigInfo는 kubeconfig에서 읽은 정보를 담습니다.
type KubeconfigInfo struct {
	CurrentContext string
	Contexts       []string
	ClusterName    string
	UserName       string
}

// LoadKubeconfigInfo는 kubeconfig 파일에서 context 정보를 로드합니다.
func LoadKubeconfigInfo(kubeconfigPath string) (*KubeconfigInfo, error) {
	if kubeconfigPath == "" {
		return nil, fmt.Errorf("kubeconfig 경로가 비어있음")
	}

	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath}
	configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
	config, err := configLoader.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig 로드 실패: %w", err)
	}

	if config.CurrentContext == "" {
		return nil, fmt.Errorf("kubeconfig에 current-context가 설정되지 않음")
	}

	// 사용 가능한 context 목록
	contexts := make([]string, 0, len(config.Contexts))
	for name := range config.Contexts {
		contexts = append(contexts, name)
	}
	sort.Strings(contexts)

	// 현재 context의 cluster/user 정보
	currentCtx := config.Contexts[config.CurrentContext]
	clusterName := ""
	userName := ""
	if currentCtx != nil {
		clusterName = currentCtx.Cluster
		userName = currentCtx.AuthInfo
	}

	return &KubeconfigInfo{
		CurrentContext: config.CurrentContext,
		Contexts:       contexts,
		ClusterName:    clusterName,
		UserName:       userName,
	}, nil
}

// SwitchContext는 kubeconfig의 current-context를 변경합니다.
func SwitchContext(kubeconfigPath string, contextName string) error {
	loadingRules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfigPath}
	configLoader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{})
	config, err := configLoader.RawConfig()
	if err != nil {
		return fmt.Errorf("kubeconfig 로드 실패: %w", err)
	}

	if _, exists := config.Contexts[contextName]; !exists {
		return fmt.Errorf("context가 존재하지 않음: %s", contextName)
	}

	config.CurrentContext = contextName
	return clientcmd.WriteToFile(config, kubeconfigPath)
}

// GetPromptPrefix는 프롬프트 프리픽스를 생성합니다.
// 예: "admin@kubernetes"
func GetPromptPrefix(info *KubeconfigInfo) string {
	if info == nil {
		return "unknown"
	}
	if info.UserName != "" && info.ClusterName != "" {
		return fmt.Sprintf("%s@%s", info.UserName, info.ClusterName)
	}
	if info.UserName != "" {
		return info.UserName
	}
	if info.ClusterName != "" {
		return info.ClusterName
	}
	return info.CurrentContext
}
