package main

import (
	"context"
	"sync"
)

type currentCommitGateState uint8

const (
	currentCommitOpen currentCommitGateState = iota
	currentCommitBegun
	currentCommitDenied
)

type currentCommitGate struct {
	mutex sync.Mutex
	state currentCommitGateState
}

type currentCommitGateContextKey struct{}

func withNewCurrentCommitGate(
	ctx context.Context,
) (context.Context, *currentCommitGate) {
	gate := &currentCommitGate{}
	return context.WithValue(
		ctx, currentCommitGateContextKey{}, gate,
	), gate
}

func ensureCurrentCommitGate(
	ctx context.Context,
) (context.Context, *currentCommitGate) {
	if gate := currentCommitGateFromContext(ctx); gate != nil {
		return ctx, gate
	}
	return withNewCurrentCommitGate(ctx)
}

// beginCurrentCommit performs the one-way arbitration immediately before the
// store's logical current-selection rename. A false result permanently denies
// that request from publishing a new current generation.
func beginCurrentCommit(ctx context.Context) bool {
	gate := currentCommitGateFromContext(ctx)
	if gate == nil {
		return false
	}
	return gate.begin(ctx)
}

func currentCommitGateFromContext(
	ctx context.Context,
) *currentCommitGate {
	if ctx == nil {
		return nil
	}
	gate, _ := ctx.Value(
		currentCommitGateContextKey{},
	).(*currentCommitGate)
	return gate
}

func (gate *currentCommitGate) begin(ctx context.Context) bool {
	gate.mutex.Lock()
	defer gate.mutex.Unlock()
	if gate.state != currentCommitOpen {
		return false
	}
	if ctx == nil || ctx.Err() != nil {
		gate.state = currentCommitDenied
		return false
	}
	gate.state = currentCommitBegun
	return true
}

func (gate *currentCommitGate) deny() {
	gate.mutex.Lock()
	defer gate.mutex.Unlock()
	if gate.state == currentCommitOpen {
		gate.state = currentCommitDenied
	}
}

func (gate *currentCommitGate) begun() bool {
	gate.mutex.Lock()
	defer gate.mutex.Unlock()
	return gate.state == currentCommitBegun
}
