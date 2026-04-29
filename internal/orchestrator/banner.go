package orchestrator

import "fmt"

func printBanner() {
	g := colorBrightGreen
	c := colorBrightCyan
	y := colorYellow
	d := colorGreenDim
	r := colorReset

	fmt.Println()
	fmt.Printf("  %s██╗  ██╗██╗███╗  ██╗██╗  ██╗%s\n", g, r)
	fmt.Printf("  %s██║ ██╔╝██║████╗ ██║╚██╗██╔╝%s        %s◆%s\n", g, r, c, r)
	fmt.Printf("  %s█████╔╝ ██║██╔██╗██║ ╚███╔╝ %s     %s───◈───%s\n", g, r, c, r)
	fmt.Printf("  %s██╔═██╗ ██║██║╚████║ ██╔██╗ %s        %s◆%s\n", g, r, c, r)
	fmt.Printf("  %s██║  ██╗██║██║ ╚███║██╔╝ ██╗%s\n", g, r)
	fmt.Printf("  %s╚═╝  ╚═╝╚═╝╚═╝  ╚══╝╚═╝  ╚═╝%s\n", g, r)
	fmt.Printf("  %s──────────────────────────────────────────────%s\n", d, r)
	fmt.Printf("  %sK8s AI Assistant%s              %swww.kinx.net%s\n", colorBold+colorBrightCyan, r, y, r)
	fmt.Printf("  %sPowered by Claude · Gemini · GPT · Ollama%s\n", d, r)
	fmt.Printf("  %s종료: exit 또는 Ctrl+C%s\n", d, r)
	fmt.Println()
}
