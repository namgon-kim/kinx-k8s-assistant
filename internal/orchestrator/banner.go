package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/k8s"
)

func PrintBanner(kubeconfigInfo *k8s.KubeconfigInfo, kubeconfigPath string) {
	g := "\033[92m"             // Bright green for KINX (bright, not bold+standard)
	blue := "\033[94m"          // Bright blue for K8s logo
	darkYellow := "\033[33m"    // Dark yellow for www.kinx.net
	cloudDevColor := "\033[97m" // Bright white for CLOUD DEV (elegant and visible)
	normal := "\033[90m"        // Gray for labels (visible on both black and white backgrounds)
	brightYellow := "\033[93m"  // Bright yellow for kimnamgon
	emphasize := "\033[32m"     // Dark green for K8s AI Assistant (sophisticated tone)
	border := "\033[32m"        // Standard green for borders (dim was too dark)
	r := colorReset

	fmt.Println()
	fmt.Printf("%s            ■■■■■■                  %s\n", blue, r)
	fmt.Printf("%s        ■■■■■■■■■■■■■■              %s┌─────────────────────────────────────────────────────────────┐%s\n", blue, border, r)
	fmt.Printf("%s    ■■■■■■■■■■-━■■■■■■■■■■          %s│                                                             │%s\n", blue, border, r)
	fmt.Printf("%s   ■■■■■■■■■■━-·━■■■■■■■■■■         %s│  %s██╗  ██╗██╗███╗  ██╗██╗  ██╗%s%s                               │%s\n", blue, border, g, r, border, r)
	fmt.Printf("%s  ■■■■━--━·· ·· -· -━·━■■■■         %s│  %s██║ ██╔╝██║████╗ ██║╚██╗██╔╝%s%s                               │%s\n", blue, border, g, r, border, r)
	fmt.Printf("%s  ■■■■■■· ·-■■ ·■■━  ·■■■■■■        %s│  %s█████╔╝ ██║██╔██╗██║ ╚███╔╝%s    %sKINX K8S AI Assistant%s%s       │%s\n", blue, border, g, r, emphasize, r, border, r)
	fmt.Printf("%s ■■■■■■- --  ·  ···-━ -■■■■■■       %s│  %s██╔═██╗ ██║██║╚████║ ██╔██╗%s        %swww.kinx.net%s%s            │%s\n", blue, border, g, r, darkYellow, r, border, r)
	fmt.Printf("%s ■■■■■■··■━━  ━-  -━■··■■■■■■       %s│  %s██║  ██╗██║██║ ╚███║██╔╝ ██╗%s%s                               │%s\n", blue, border, g, r, border, r)
	fmt.Printf("%s■■■■━--  ···- ·· ·-··  --━■■■■      %s│  %s╚═╝  ╚═╝╚═╝╚═╝  ╚══╝╚═╝  ╚═╝%s%s                               │%s\n", blue, border, g, r, border, r)
	fmt.Printf("%s■■■■■━■■··■■- -- -■■··━■━■■■■■      %s│%s                                                             %s│%s\n", blue, border, r, border, r)
	fmt.Printf("%s ■■■■■■■■- · ·━■- · ·■■■■■■■■       %s│         %sCLOUD DEV%s%s                     %sCreated by %skimnamgon%s%s  │%s\n", blue, border, cloudDevColor, r, border, normal, brightYellow, r, border, r)
	fmt.Printf("%s   ■■■■■■■━ ·-···· ━■■■■■■■         %s└─────────────────────────────────────────────────────────────┘%s\n", blue, border, r)
	fmt.Printf("%s    ■■■■■■·-■■■■■■━·■■■■■■          %s\n", blue, r)
	fmt.Printf("%s      ■■■■■■■■■■■■■■■■■■            %s\n", blue, r)
	fmt.Printf("%s       ■■■■■■■■■■■■■■■■             %s\n", blue, r)

	fmt.Println()
	fmt.Printf("  %s──────────────────────────────────────────────────────────────%s\n", border, r)

	// kubeconfig 정보 표시
	if kubeconfigPath != "" {
		expandedPath := kubeconfigPath
		if strings.HasPrefix(kubeconfigPath, "~") {
			home, _ := os.UserHomeDir()
			expandedPath = filepath.Join(home, kubeconfigPath[1:])
		}

		if _, err := os.Stat(expandedPath); err == nil {
			fmt.Printf("  %sKubeconfig:%s %s%s%s\n", normal, r, brightYellow, kubeconfigPath, r)
		} else {
			fmt.Printf("  %sKubeconfig:%s %s%s (not found)%s\n", normal, r, "\033[31m", kubeconfigPath, r)
		}
	} else {
		fmt.Printf("  %sKubeconfig:%s %s(not configured - use /kubeconfig to set)%s\n", normal, r, "\033[31m", r)
	}

	if kubeconfigInfo != nil {
		fmt.Printf("  %sContext:%s       %s%s%s\n", normal, r, emphasize, kubeconfigInfo.CurrentContext, r)
	}

	fmt.Printf("  %s──────────────────────────────────────────────────────────────%s\n", border, r)
	fmt.Printf("  %sPowered by %sClaude%s · %sGemini%s · %sGPT%s · %sOllama%s\n", normal, colorBrightCyan, r, colorBrightCyan, r, colorBrightCyan, r, colorBrightCyan, r)
	fmt.Printf("  %sExit: %sexit%s or %sCtrl+C%s\n", normal, emphasize, r, emphasize, r)

	// 상태 표시
	statusMsg := "✓ Ready"
	statusColor := "\033[96m" // 밝은 파란색
	if kubeconfigInfo == nil && kubeconfigPath != "" {
		statusMsg = "⚠ kubeconfig not found"
		statusColor = "\033[31m" // 빨간색
	} else if kubeconfigInfo == nil {
		statusMsg = "⚠ kubeconfig not configured"
		statusColor = "\033[31m" // 빨간색
	}
	fmt.Printf("  %s%s%s\n", statusColor, statusMsg, r)
	fmt.Println()
}
