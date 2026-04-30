//go:build windows

package main

import (
	"fmt"
	"sync"

	"detour/internal/rules"
)

// manager owns a set of engines (one per rule) and coordinates with the
// rules.Store for persistence. All rule mutations go through here so the
// store and the engine map stay in sync.
//
// onChanged is invoked whenever the rule list or any engine's observable
// state moves. The GUI sets this to fire model.refresh + PublishRowsReset
// on the GUI thread; the callback runs on whichever goroutine triggered
// the change, so the GUI binding must marshal back via Synchronize.
type manager struct {
	store *rules.Store

	mu      sync.RWMutex
	engines map[string]*engine
	order   []string // rule IDs in display order

	cbMu      sync.Mutex
	onChanged func()
}

func newManager(store *rules.Store) *manager {
	return &manager{
		store:   store,
		engines: map[string]*engine{},
	}
}

// SetOnChanged installs the change callback. Replaces any previous one.
func (m *manager) SetOnChanged(fn func()) {
	m.cbMu.Lock()
	m.onChanged = fn
	m.cbMu.Unlock()
}

func (m *manager) notify() {
	m.cbMu.Lock()
	fn := m.onChanged
	m.cbMu.Unlock()
	if fn != nil {
		fn()
	}
}

// LoadFromStore syncs the engine map with whatever the store currently
// holds. Existing engines are discarded — callers should StopAll first if
// any are running.
func (m *manager) LoadFromStore() {
	snap := m.store.Snapshot()
	m.mu.Lock()
	m.engines = make(map[string]*engine, len(snap))
	m.order = make([]string, 0, len(snap))
	for _, r := range snap {
		m.engines[r.ID] = newEngine(r)
		m.order = append(m.order, r.ID)
	}
	m.mu.Unlock()
	m.notify()
}

// Add validates and persists a new rule via the store, then creates an
// engine for it. The returned Rule carries the assigned ID.
func (m *manager) Add(r rules.Rule) (rules.Rule, error) {
	added, err := m.store.Add(r)
	if err != nil {
		return rules.Rule{}, err
	}
	m.mu.Lock()
	m.engines[added.ID] = newEngine(added)
	m.order = append(m.order, added.ID)
	m.mu.Unlock()
	m.notify()
	return added, nil
}

// Update applies a new spec to an existing rule. Returns an error if the
// engine is currently running — the caller must Stop it first to avoid
// silent traffic redirection changes.
func (m *manager) Update(r rules.Rule) error {
	m.mu.RLock()
	e, ok := m.engines[r.ID]
	m.mu.RUnlock()
	if !ok {
		return rules.ErrNotFound
	}
	if e.isRunning() {
		return fmt.Errorf("rule %s is running — stop it before editing", r.ID)
	}
	if err := m.store.Update(r); err != nil {
		return err
	}
	e.updateRule(r)
	m.notify()
	return nil
}

// Remove stops the engine if running and deletes the rule from the store.
// The engine's runtime.Run goroutine drains asynchronously; it doesn't
// reference manager state so dropping the engine reference is safe.
func (m *manager) Remove(id string) error {
	m.mu.RLock()
	e, ok := m.engines[id]
	m.mu.RUnlock()
	if !ok {
		return rules.ErrNotFound
	}
	if e.isRunning() {
		e.Stop()
	}
	if err := m.store.Remove(id); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.engines, id)
	for i, key := range m.order {
		if key == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
	m.notify()
	return nil
}

// Start launches the engine for id. Returns nil on success, an error if
// the rule is unknown or already running.
func (m *manager) Start(id string) error {
	m.mu.RLock()
	e, ok := m.engines[id]
	m.mu.RUnlock()
	if !ok {
		return rules.ErrNotFound
	}
	if !e.Start(m.notify) {
		return fmt.Errorf("rule %s is already running", id)
	}
	m.notify()
	return nil
}

// Stop signals the engine to cancel its context. Returns immediately; the
// engine reaches idle asynchronously when runtime.Run drains.
func (m *manager) Stop(id string) error {
	m.mu.RLock()
	e, ok := m.engines[id]
	m.mu.RUnlock()
	if !ok {
		return rules.ErrNotFound
	}
	e.Stop()
	m.notify()
	return nil
}

// StopAll signals every running engine to stop. Useful for app shutdown.
func (m *manager) StopAll() {
	m.mu.RLock()
	es := make([]*engine, 0, len(m.engines))
	for _, e := range m.engines {
		es = append(es, e)
	}
	m.mu.RUnlock()
	for _, e := range es {
		e.Stop()
	}
}

// AnyRunning reports whether at least one engine is running or stopping.
func (m *manager) AnyRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.engines {
		if e.isRunning() {
			return true
		}
	}
	return false
}

// SnapshotAll returns a stable, ordered slice of engine snapshots — what
// the table model renders.
func (m *manager) SnapshotAll() []engineSnapshot {
	m.mu.RLock()
	out := make([]engineSnapshot, 0, len(m.order))
	for _, id := range m.order {
		if e, ok := m.engines[id]; ok {
			out = append(out, e.Snapshot())
		}
	}
	m.mu.RUnlock()
	return out
}

// AggregateCounts sums forward/reverse across all engines and counts
// how many are running. Used for the tray tooltip and a status bar.
func (m *manager) AggregateCounts() (running int, fwd, rev uint64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.engines {
		snap := e.Snapshot()
		fwd += snap.Forward
		rev += snap.Reverse
		if snap.State == engineRunning || snap.State == engineStopping {
			running++
		}
	}
	return
}
