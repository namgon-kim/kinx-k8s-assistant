package orchestrator

import (
	"fmt"
	"sync"
	"time"
)

const maxTurns = 10

// Turn은 대화 1턴(사용자 입력 + 에이전트 응답)을 나타냅니다.
type Turn struct {
	Index     int
	UserInput string
	Response  string
	ToolRefs  []ToolRef // 이 턴에서 사용된 Tool 결과 참조 ID 목록
	Timestamp time.Time
}

// ToolRef는 Tool 실행 결과에 대한 참조입니다.
// "아까 그 결과" 같은 대명사 참조에 활용됩니다.
type ToolRef struct {
	ID       string // "ref_0", "ref_1", ...
	ToolName string
	Summary  string // 결과 요약 (처음 100자)
}

// ConversationContext는 최근 10턴의 대화 이력과 참조 ID를 관리합니다.
type ConversationContext struct {
	mu       sync.Mutex
	turns    []*Turn
	refMap   map[string]string // refID → tool result
	refCount int
}

// NewConversationContext는 새 ConversationContext를 반환합니다.
func NewConversationContext() *ConversationContext {
	return &ConversationContext{
		turns:  make([]*Turn, 0, maxTurns),
		refMap: make(map[string]string),
	}
}

// AddTurn은 새 턴을 추가합니다. 10턴을 초과하면 가장 오래된 턴을 제거합니다.
func (c *ConversationContext) AddTurn(userInput, response string, refs []ToolRef) {
	c.mu.Lock()
	defer c.mu.Unlock()

	turn := &Turn{
		Index:     len(c.turns),
		UserInput: userInput,
		Response:  response,
		ToolRefs:  refs,
		Timestamp: time.Now(),
	}

	c.turns = append(c.turns, turn)

	if len(c.turns) > maxTurns {
		// 가장 오래된 턴 제거
		c.turns = c.turns[len(c.turns)-maxTurns:]
	}
}

// AddToolResult는 Tool 결과를 참조 ID와 함께 저장하고 ID를 반환합니다.
func (c *ConversationContext) AddToolResult(toolName, result string) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	refID := fmt.Sprintf("ref_%d", c.refCount)
	c.refCount++
	c.refMap[refID] = result

	return refID
}

// GetToolResult는 참조 ID로 Tool 결과를 반환합니다.
func (c *ConversationContext) GetToolResult(refID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	result, ok := c.refMap[refID]
	return result, ok
}

// RecentTurns는 최근 N개의 턴을 반환합니다.
func (c *ConversationContext) RecentTurns(n int) []*Turn {
	c.mu.Lock()
	defer c.mu.Unlock()

	if n > len(c.turns) {
		n = len(c.turns)
	}
	return c.turns[len(c.turns)-n:]
}

// TurnCount는 현재까지 누적된 턴 수를 반환합니다.
func (c *ConversationContext) TurnCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.turns)
}
