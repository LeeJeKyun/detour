//go:build windows

package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"detour/internal/rules"
	"detour/internal/runtime"
)

type engineState int

const (
	engineIdle engineState = iota
	engineRunning
	engineStopping
	engineError
)

func (s engineState) String() string {
	switch s {
	case engineRunning:
		return "running"
	case engineStopping:
		return "stopping"
	case engineError:
		return "error"
	default:
		return "idle"
	}
}

// engine wraps the lifecycle of runtime.Run for a single rule. It owns the
// cancel func, atomic packet counters, and any terminal error so the table
// model can render a stable view of the rule's state.
//
// engine is safe for concurrent use; the mutex covers state, cancel, and
// lastErr. Counters are atomics so polling readers don't fight the
// runtime.Run writer.
type engine struct {
	rule rules.Rule

	mu      sync.Mutex
	state   engineState
	cancel  context.CancelFunc
	lastErr error

	fwd atomic.Uint64
	rev atomic.Uint64
}

func newEngine(r rules.Rule) *engine {
	return &engine{rule: r, state: engineIdle}
}

// engineSnapshot is a value-typed view of engine state. The table model
// consumes a slice of these so rendering doesn't need to hold any lock.
type engineSnapshot struct {
	Rule    rules.Rule
	State   engineState
	Forward uint64
	Reverse uint64
	LastErr error
}

// Start launches runtime.Run on a goroutine. onChange (if non-nil) is
// invoked whenever the engine's observable state changes (transition out
// of running, error captured). Returns false if the engine is already
// running or stopping — the caller should treat that as "already started"
// rather than an error.
func (e *engine) Start(onChange func()) bool {
	e.mu.Lock()
	if e.state == engineRunning || e.state == engineStopping {
		e.mu.Unlock()
		return false
	}
	e.state = engineRunning
	e.lastErr = nil
	e.fwd.Store(0)
	e.rev.Store(0)
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	rule := e.rule
	e.mu.Unlock()

	go func() {
		err := runtime.Run(ctx, runtime.Rule{
			From:  rule.From,
			To:    rule.To,
			Proto: rule.Proto,
		}, runtime.Options{
			ForwardCounter: &e.fwd,
			ReverseCounter: &e.rev,
		})

		e.mu.Lock()
		switch {
		case err != nil && !errors.Is(err, context.Canceled):
			e.lastErr = err
			e.state = engineError
		default:
			e.state = engineIdle
		}
		e.cancel = nil
		e.mu.Unlock()
		if onChange != nil {
			onChange()
		}
	}()
	return true
}

// Stop cancels the running context. The actual teardown happens on the
// runtime.Run goroutine; isRunning will read false once that returns.
func (e *engine) Stop() {
	e.mu.Lock()
	if e.cancel != nil && e.state == engineRunning {
		e.state = engineStopping
		e.cancel()
	}
	e.mu.Unlock()
}

// Snapshot returns a value copy of the engine's current state. Cheap to
// call repeatedly — used by the GUI poll loop.
func (e *engine) Snapshot() engineSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return engineSnapshot{
		Rule:    e.rule,
		State:   e.state,
		Forward: e.fwd.Load(),
		Reverse: e.rev.Load(),
		LastErr: e.lastErr,
	}
}

// updateRule replaces the rule spec while idle. Returns false if the
// engine is running — caller must Stop first.
func (e *engine) updateRule(r rules.Rule) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state == engineRunning || e.state == engineStopping {
		return false
	}
	e.rule = r
	return true
}

// isRunning reports whether runtime.Run is in flight (or in the process
// of shutting down).
func (e *engine) isRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state == engineRunning || e.state == engineStopping
}
