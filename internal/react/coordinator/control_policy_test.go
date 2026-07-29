package coordinator

import (
	"context"
	"strings"
	"testing"

	"github.com/namgon-kim/kinx-k8s-assistant/internal/react/contract"
)

func TestExecuteIterationRejectsEveryNonModelControlState(t *testing.T) {
	for _, state := range contract.AllRuntimeControlStates() {
		class, ok := contract.ClassifyRuntimeControlState(state)
		if !ok || class == contract.ControlExecutionModelTurn {
			continue
		}
		t.Run(string(state), func(t *testing.T) {
			loop := &Loop{runtimeState: &runtimeState{control: state}}
			err := loop.executeIteration(context.Background())
			if err == nil || !strings.Contains(err.Error(), "model turn is not allowed") {
				t.Fatalf("executeIteration error = %v", err)
			}
		})
	}
}

func TestRuntimeSnapshotAuditRejectsUnknownControlState(t *testing.T) {
	snapshot := RuntimeSnapshot{Control: RuntimeControlState("future_state")}
	if got := snapshot.AuditError(); !strings.Contains(got, "unknown") {
		t.Fatalf("audit error = %q, want unknown control rejection", got)
	}
}
