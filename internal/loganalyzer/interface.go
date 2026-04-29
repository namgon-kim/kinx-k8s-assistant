// Package loganalyzer는 컨테이너 로그 분석 및 RAG 기반 트러블슈팅 기능을 제공합니다.
//
// 현재 상태: 기획 단계 (인터페이스 정의만 완료)
// 구현 예정:
//   - Filebeat 수집 로그 파일 접근
//   - 로그 패턴 분석 (에러 스파이크, OOMKilled, CrashLoop 등)
//   - RAG 기반 유사 장애 사례 검색 (벡터 DB 활용)
//   - 인프라 환경별 대처 방안 제시 (Runbook, 조치 가이드)
//
// 이 패키지는 kubectl-ai의 MCP 클라이언트 모드를 통해 연동됩니다.
// kubectl-ai --mcp-client 실행 시 ~/.config/kubectl-ai/mcp.yaml의
// log-analyzer 서버가 자동으로 연결됩니다.
package loganalyzer

import "context"

// Analyzer는 로그 분석 기능의 인터페이스를 정의합니다.
type Analyzer interface {
	// FetchLogs는 지정된 Pod/컨테이너의 수집된 로그를 반환합니다.
	// Filebeat 등 로그 수집 에이전트가 저장한 파일에서 읽습니다.
	FetchLogs(ctx context.Context, req FetchLogsRequest) (*FetchLogsResult, error)

	// AnalyzePattern은 로그에서 이상 패턴을 탐지합니다.
	// CrashLoop, OOMKilled, 에러 스파이크 등을 감지합니다.
	AnalyzePattern(ctx context.Context, req AnalyzePatternRequest) (*AnalyzePatternResult, error)

	// RAGLookup은 증상 설명으로 유사 장애 사례를 검색합니다.
	RAGLookup(ctx context.Context, req RAGLookupRequest) (*RAGLookupResult, error)

	// AnalyzeAndRemediate는 FetchLogs → AnalyzePattern → RAGLookup → 조치 제안을
	// 하나의 파이프라인으로 실행합니다.
	AnalyzeAndRemediate(ctx context.Context, req RemediateRequest) (*RemediateResult, error)
}

// --- Request/Result 타입 ---

// FetchLogsRequest는 로그 조회 요청입니다.
type FetchLogsRequest struct {
	Namespace     string
	PodName       string
	ContainerName string
	// 조회 시간 범위 (Unix timestamp)
	SinceSeconds int64
	MaxLines     int
}

// FetchLogsResult는 로그 조회 결과입니다.
type FetchLogsResult struct {
	Logs      []LogEntry
	TotalLine int
	Source    string // 로그 파일 경로
}

// LogEntry는 단일 로그 라인입니다.
type LogEntry struct {
	Timestamp string
	Level     string // INFO, WARN, ERROR, FATAL
	Message   string
	Raw       string
}

// AnalyzePatternRequest는 패턴 분석 요청입니다.
type AnalyzePatternRequest struct {
	Logs      []LogEntry
	PodName   string
	Namespace string
}

// AnalyzePatternResult는 패턴 분석 결과입니다.
type AnalyzePatternResult struct {
	Patterns  []DetectedPattern
	Severity  string // critical, warning, info
	Summary   string
}

// DetectedPattern은 탐지된 이상 패턴입니다.
type DetectedPattern struct {
	Type        PatternType
	Description string
	Count       int
	Timestamps  []string
}

// PatternType은 이상 패턴 종류입니다.
type PatternType string

const (
	PatternCrashLoop   PatternType = "CrashLoop"
	PatternOOMKilled   PatternType = "OOMKilled"
	PatternErrorSpike  PatternType = "ErrorSpike"
	PatternSlowLatency PatternType = "SlowLatency"
	PatternDiskFull    PatternType = "DiskFull"
)

// RAGLookupRequest는 RAG 검색 요청입니다.
type RAGLookupRequest struct {
	Symptom    string   // 증상 설명 (자연어)
	Patterns   []string // 탐지된 패턴 목록
	MaxResults int
}

// RAGLookupResult는 RAG 검색 결과입니다.
type RAGLookupResult struct {
	Cases []SimilarCase
}

// SimilarCase는 유사 장애 사례입니다.
type SimilarCase struct {
	Title      string
	Similarity float64
	Cause      string
	Resolution string
	Source     string // Runbook URL 또는 사내 문서 경로
}

// RemediateRequest는 통합 분석 및 조치 요청입니다.
type RemediateRequest struct {
	Namespace     string
	PodName       string
	ContainerName string
	SinceSeconds  int64
}

// RemediateResult는 통합 분석 및 조치 결과입니다.
type RemediateResult struct {
	Summary      string
	Patterns     []DetectedPattern
	SimilarCases []SimilarCase
	Remediation  []RemediationStep
	Confidence   ConfidenceLevel
}

// RemediationStep은 권장 조치 단계입니다.
type RemediationStep struct {
	Order       int
	Description string
	Command     string // 실행 가능한 kubectl 명령 (있는 경우)
	IsAutomatic bool   // true이면 자동 실행 가능
}

// ConfidenceLevel은 진단 확신 수준입니다 (5단계).
type ConfidenceLevel string

const (
	ConfidenceCertain   ConfidenceLevel = "확실"
	ConfidenceHigh      ConfidenceLevel = "높음"
	ConfidenceMedium    ConfidenceLevel = "중간"
	ConfidenceLow       ConfidenceLevel = "낮음"
	ConfidenceSpeculate ConfidenceLevel = "추측"
)
