package loganalyzer

import (
	"strconv"
	"strings"
	"time"
)

type PatternDetector struct{}

func NewPatternDetector() *PatternDetector {
	return &PatternDetector{}
}

func (d *PatternDetector) Detect(logs []LogEntry, podName, namespace string) *AnalyzePatternResult {
	if len(logs) == 0 {
		return &AnalyzePatternResult{
			Patterns: []DetectedPattern{},
			Severity: "info",
			Summary:  "로그가 없습니다",
		}
	}

	patterns := []DetectedPattern{}

	patterns = append(patterns, d.detectCrashLoop(logs)...)
	patterns = append(patterns, d.detectOOMKilled(logs)...)
	patterns = append(patterns, d.detectErrorSpike(logs)...)
	patterns = append(patterns, d.detectSlowLatency(logs)...)
	patterns = append(patterns, d.detectDiskFull(logs)...)

	severity := "info"
	if len(patterns) > 0 {
		severity = "warning"
		for _, p := range patterns {
			if p.Type == PatternOOMKilled || p.Type == PatternDiskFull {
				severity = "critical"
				break
			}
		}
	}

	summary := generateSummary(patterns, podName)

	return &AnalyzePatternResult{
		Patterns: patterns,
		Severity: severity,
		Summary:  summary,
	}
}

func (d *PatternDetector) detectCrashLoop(logs []LogEntry) []DetectedPattern {
	var pattern DetectedPattern
	pattern.Type = PatternCrashLoop

	for _, log := range logs {
		msg := strings.ToLower(log.Message)
		if strings.Contains(msg, "back-off") || strings.Contains(msg, "backoff") {
			pattern.Count++
			pattern.Timestamps = append(pattern.Timestamps, log.Timestamp)
		}
	}

	if pattern.Count > 0 {
		pattern.Description = "Pod이 반복적으로 재시작되고 있습니다 (CrashLoopBackOff)"
		return []DetectedPattern{pattern}
	}
	return []DetectedPattern{}
}

func (d *PatternDetector) detectOOMKilled(logs []LogEntry) []DetectedPattern {
	var pattern DetectedPattern
	pattern.Type = PatternOOMKilled

	for _, log := range logs {
		msg := strings.ToLower(log.Message)
		if strings.Contains(msg, "oomkilled") {
			pattern.Count++
			pattern.Timestamps = append(pattern.Timestamps, log.Timestamp)
		}
	}

	if pattern.Count > 0 {
		pattern.Description = "메모리 부족으로 Pod이 종료되었습니다"
		return []DetectedPattern{pattern}
	}
	return []DetectedPattern{}
}

func (d *PatternDetector) detectErrorSpike(logs []LogEntry) []DetectedPattern {
	if len(logs) < 10 {
		return []DetectedPattern{}
	}

	const windowSize = 100
	const errorThreshold = 0.3

	var pattern DetectedPattern
	pattern.Type = PatternErrorSpike

	for i := 0; i <= len(logs)-windowSize; i++ {
		window := logs[i : i+windowSize]
		errorCount := 0
		for _, log := range window {
			if log.Level == "ERROR" || log.Level == "FATAL" {
				errorCount++
			}
		}
		errorRate := float64(errorCount) / float64(len(window))
		if errorRate > errorThreshold {
			pattern.Count++
			if len(window) > 0 {
				pattern.Timestamps = append(pattern.Timestamps, window[0].Timestamp)
			}
		}
	}

	if pattern.Count > 0 {
		pattern.Description = "에러 로그가 비정상적으로 많이 발생하고 있습니다"
		return []DetectedPattern{pattern}
	}
	return []DetectedPattern{}
}

func (d *PatternDetector) detectSlowLatency(logs []LogEntry) []DetectedPattern {
	var pattern DetectedPattern
	pattern.Type = PatternSlowLatency

	for _, log := range logs {
		msg := strings.ToLower(log.Message)
		if strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline") {
			pattern.Count++
			pattern.Timestamps = append(pattern.Timestamps, log.Timestamp)
		}
	}

	if pattern.Count > 0 {
		pattern.Description = "응답 시간 초과(timeout) 또는 느린 응답이 감지되었습니다"
		return []DetectedPattern{pattern}
	}
	return []DetectedPattern{}
}

func (d *PatternDetector) detectDiskFull(logs []LogEntry) []DetectedPattern {
	var pattern DetectedPattern
	pattern.Type = PatternDiskFull

	for _, log := range logs {
		msg := strings.ToLower(log.Message)
		if strings.Contains(msg, "no space left") || strings.Contains(msg, "disk full") {
			pattern.Count++
			pattern.Timestamps = append(pattern.Timestamps, log.Timestamp)
		}
	}

	if pattern.Count > 0 {
		pattern.Description = "디스크 공간이 부족합니다"
		return []DetectedPattern{pattern}
	}
	return []DetectedPattern{}
}

func generateSummary(patterns []DetectedPattern, podName string) string {
	if len(patterns) == 0 {
		return podName + " Pod에서 이상 패턴이 감지되지 않았습니다"
	}

	var sb strings.Builder
	sb.WriteString(podName + " Pod에서 ")
	sb.WriteString(time.Now().Format("2006-01-02 15:04:05"))
	sb.WriteString(" 기준 ")
	sb.WriteString(strconv.Itoa(len(patterns)))
	sb.WriteString("개의 이상 패턴이 감지되었습니다: ")

	typeNames := make([]string, len(patterns))
	for i, p := range patterns {
		typeNames[i] = string(p.Type)
	}
	sb.WriteString(strings.Join(typeNames, ", "))

	return sb.String()
}
