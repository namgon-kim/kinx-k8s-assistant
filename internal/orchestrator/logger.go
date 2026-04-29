package orchestrator

import (
	"fmt"
	"os"
	"time"
)

// Logger는 대화 로그를 파일에 평문으로 저장합니다.
type Logger struct {
	file *os.File
}

// NewLogger는 지정된 경로에 로그 파일을 열고 Logger를 반환합니다.
func NewLogger(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &Logger{file: f}, nil
}

// Write는 로그 항목을 기록합니다.
func (l *Logger) Write(kind, content string) {
	if l == nil || l.file == nil {
		return
	}
	ts := time.Now().Format(time.RFC3339)
	line := fmt.Sprintf("[%s] [%s] %s\n", ts, kind, content)
	_, _ = l.file.WriteString(line)
}

// Close는 로그 파일을 닫습니다.
func (l *Logger) Close() {
	if l != nil && l.file != nil {
		_ = l.file.Close()
	}
}
