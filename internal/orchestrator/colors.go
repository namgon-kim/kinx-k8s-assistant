package orchestrator

// ANSI color codes — 검정/흰색 배경 모두에서 가시성 확보
const (
	colorReset = "\033[0m"
	// colorBold  = "\033[1m"
	//colorDim           = "\033[2m"
	colorBrightGreen = "\033[1;92m" // 배너 KINX 텍스트
	//colorBrightBlue    = "\033[1;94m" // Kubernetes 심볼
	colorBrightCyan      = "\033[96m"   // crosshair / 툴 결과
	colorBrightMagenta   = "\033[95m"   // 툴 호출 (실행 중) — 검정/흰색 배경 모두 가시
	colorBrightMagentaBg = "\033[1;95m" // 강조 표시용
	// colorGreenDim        = "\033[2;32m" // 구분선
	colorYellow    = "\033[93m" // 배너 사이트/강조
	colorBrightRed = "\033[91m" // 오류 표시
)
