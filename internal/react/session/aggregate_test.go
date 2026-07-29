package session

import (
	"errors"
	"testing"
)

type aggregateFixture struct {
	Control string
	Values  []string
}

func cloneAggregateFixture(source aggregateFixture) aggregateFixture {
	clone := source
	clone.Values = append([]string(nil), source.Values...)
	return clone
}

func TestAggregateCommitIsAtomicAndRevisioned(t *testing.T) {
	aggregate := NewAggregate(aggregateFixture{Control: "before", Values: []string{"a"}})
	expected, candidate := aggregate.Candidate(cloneAggregateFixture)
	candidate.Control = "after"
	candidate.Values[0] = "b"

	revision, err := aggregate.Commit(expected, candidate, func(candidate aggregateFixture, candidateRevision uint64) error {
		if candidate.Control != "after" {
			return errors.New("candidate was not provided to audit")
		}
		if candidateRevision != 1 {
			t.Fatalf("candidate revision = %d, want 1", candidateRevision)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("commit candidate: %v", err)
	}
	if revision != 1 || aggregate.Revision() != 1 {
		t.Fatalf("revision = %d/%d, want 1", revision, aggregate.Revision())
	}
	if root := aggregate.Root(); root.Control != "after" || root.Values[0] != "b" {
		t.Fatalf("root = %#v", root)
	}
}

func TestAggregateRejectsAuditAndStaleRevisionWithoutChangingRoot(t *testing.T) {
	aggregate := NewAggregate(aggregateFixture{Control: "before"})
	expected, rejected := aggregate.Candidate(cloneAggregateFixture)
	rejected.Control = "invalid"
	if _, err := aggregate.Commit(expected, rejected, func(aggregateFixture, uint64) error { return errors.New("invalid") }); err == nil {
		t.Fatal("audit failure was accepted")
	}
	if aggregate.Revision() != 0 || aggregate.Root().Control != "before" {
		t.Fatalf("audit failure changed aggregate: revision=%d root=%#v", aggregate.Revision(), aggregate.Root())
	}

	_, first := aggregate.Candidate(cloneAggregateFixture)
	first.Control = "first"
	if _, err := aggregate.Commit(0, first, nil); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	stale := aggregateFixture{Control: "stale"}
	if _, err := aggregate.Commit(0, &stale, nil); err == nil {
		t.Fatal("stale commit was accepted")
	}
	if aggregate.Revision() != 1 || aggregate.Root().Control != "first" {
		t.Fatalf("stale commit changed aggregate: revision=%d root=%#v", aggregate.Revision(), aggregate.Root())
	}
}
