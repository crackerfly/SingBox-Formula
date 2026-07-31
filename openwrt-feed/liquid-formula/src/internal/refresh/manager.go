// Package refresh serializes cache/state refresh transactions.
package refresh

import (
	"context"
	"errors"
)

var ErrNilWork = errors.New("refresh work is nil")

// Manager is a context-aware, single-permit coordinator. Unlike a mutex, a
// caller waiting for the permit can abandon the wait when its context ends.
type Manager struct {
	permit chan struct{}
}

func NewManager() *Manager {
	permit := make(chan struct{}, 1)
	permit <- struct{}{}
	return &Manager{permit: permit}
}

// Do runs work as the sole active refresh writer. Trigger is retained at the
// boundary for observability by callers; serialization itself is trigger-agnostic.
func (m *Manager) Do(ctx context.Context, trigger string, work func(context.Context) error) error {
	_ = trigger
	if work == nil {
		return ErrNilWork
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if m == nil || m.permit == nil {
		return errors.New("refresh manager is not initialized")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.permit:
	}
	defer func() { m.permit <- struct{}{} }()

	if err := ctx.Err(); err != nil {
		return err
	}
	return work(ctx)
}
