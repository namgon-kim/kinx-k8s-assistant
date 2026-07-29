// Package session owns the revisioned mutable workflow root for one ReAct session.
package session

import (
	"fmt"
	"sync"
)

// Aggregate owns one revisioned mutable root. Callers construct and audit a
// detached candidate, then replace the root with one compare-and-swap commit.
type Aggregate[T any] struct {
	mu       sync.RWMutex
	revision uint64
	root     *T
}

func NewAggregate[T any](initial T) *Aggregate[T] {
	root := initial
	return &Aggregate[T]{root: &root}
}

func (a *Aggregate[T]) Root() *T {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.root
}

func (a *Aggregate[T]) Revision() uint64 {
	if a == nil {
		return 0
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.revision
}

func (a *Aggregate[T]) Candidate(clone func(T) T) (uint64, *T) {
	if a == nil || clone == nil {
		return 0, nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	candidate := clone(*a.root)
	return a.revision, &candidate
}

func (a *Aggregate[T]) Commit(expected uint64, candidate *T, audit func(T) error) (uint64, error) {
	if a == nil || candidate == nil {
		return 0, fmt.Errorf("session candidate is nil")
	}
	if audit != nil {
		if err := audit(*candidate); err != nil {
			return a.Revision(), err
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revision != expected {
		return a.revision, fmt.Errorf("stale session revision: expected %d, current %d", expected, a.revision)
	}
	a.root = candidate
	a.revision++
	return a.revision, nil
}
