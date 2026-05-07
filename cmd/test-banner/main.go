package main

import (
	"os"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/k8s"
	"github.com/namgon-kim/kinx-k8s-assistant/internal/orchestrator"
)

func main() {
	// 테스트용 kubeconfig 경로 (환경변수 또는 기본값)
	kubeconfigPath := os.Getenv("KUBECONFIG")
	if kubeconfigPath == "" {
		kubeconfigPath = os.ExpandEnv("$HOME/.kube/config")
	}

	// kubeconfig 정보 로드 (실패해도 괜찮음 - nil로 전달)
	var kubeconfigInfo *k8s.KubeconfigInfo
	if info, err := k8s.LoadKubeconfigInfo(kubeconfigPath); err == nil {
		kubeconfigInfo = info
	}

	// 배너만 출력
	orchestrator.PrintBanner(kubeconfigInfo, kubeconfigPath)
}
