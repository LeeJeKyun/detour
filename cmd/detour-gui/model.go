//go:build windows

package main

import (
	"fmt"

	"github.com/lxn/walk"
)

const (
	colActive = iota
	colFrom
	colTo
	colProto
	colForward
	colReverse
	colStatus
)

// ruleTableModel binds the manager to a walk.TableView. The model holds a
// snapshot taken at refresh time so cell rendering doesn't fight manager
// locks per Value() call. ItemChecker turns the leftmost column into a
// row-selection checkbox — actual Start/Stop happens via footer buttons
// that act on the currently checked rows.
type ruleTableModel struct {
	walk.TableModelBase
	mgr  *manager
	rows []engineSnapshot

	// selected tracks which rule IDs have their row checkbox ticked.
	// Survives PublishRowsReset because walk re-queries Checked() from
	// this map after the reset.
	selected map[string]bool

	// onSelectionChanged fires after the user toggles a checkbox so the
	// GUI can recompute Start/Stop/Edit/Delete enabled state. Invoked on
	// the GUI thread (SetChecked is a walk message-loop callback).
	onSelectionChanged func()
}

func newRuleTableModel(mgr *manager) *ruleTableModel {
	m := &ruleTableModel{mgr: mgr, selected: map[string]bool{}}
	m.refresh()
	return m
}

// refresh re-reads from the manager. Call from the GUI thread before
// PublishRowsReset so the table picks up the new snapshot. Also prunes
// stale entries from `selected` for rules that were removed.
func (m *ruleTableModel) refresh() {
	m.rows = m.mgr.SnapshotAll()
	if len(m.selected) == 0 {
		return
	}
	live := make(map[string]struct{}, len(m.rows))
	for _, r := range m.rows {
		live[r.Rule.ID] = struct{}{}
	}
	for id := range m.selected {
		if _, ok := live[id]; !ok {
			delete(m.selected, id)
		}
	}
}

func (m *ruleTableModel) RowCount() int {
	return len(m.rows)
}

func (m *ruleTableModel) Value(row, col int) interface{} {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	s := m.rows[row]
	switch col {
	case colActive:
		// Rendered as a textual indicator next to the checkbox column;
		// the checkbox itself is driven by Checked() below.
		if s.State == engineRunning {
			return "●"
		}
		if s.State == engineStopping {
			return "…"
		}
		return ""
	case colFrom:
		return s.Rule.From.String()
	case colTo:
		return s.Rule.To.String()
	case colProto:
		return s.Rule.Proto.String()
	case colForward:
		return s.Forward
	case colReverse:
		return s.Reverse
	case colStatus:
		if s.LastErr != nil {
			return fmt.Sprintf("error: %v", s.LastErr)
		}
		return s.State.String()
	}
	return ""
}

// Checked / SetChecked satisfy walk.ItemChecker. The checkbox now
// represents row selection — it does NOT start or stop the engine
// directly. Start/Stop happens via the footer buttons, which read
// SelectedSnapshots() and act in bulk.
func (m *ruleTableModel) Checked(index int) bool {
	if index < 0 || index >= len(m.rows) {
		return false
	}
	return m.selected[m.rows[index].Rule.ID]
}

func (m *ruleTableModel) SetChecked(index int, checked bool) error {
	if index < 0 || index >= len(m.rows) {
		return nil
	}
	id := m.rows[index].Rule.ID
	if checked {
		m.selected[id] = true
	} else {
		delete(m.selected, id)
	}
	if m.onSelectionChanged != nil {
		m.onSelectionChanged()
	}
	return nil
}

// SelectedSnapshots returns the snapshots of all currently checked
// rows, preserving display order. Button handlers use this to decide
// which engines to Start/Stop and to gate Edit (single selection only).
func (m *ruleTableModel) SelectedSnapshots() []engineSnapshot {
	var out []engineSnapshot
	for _, r := range m.rows {
		if m.selected[r.Rule.ID] {
			out = append(out, r)
		}
	}
	return out
}

// ToggleAllSelected flips the selection state en masse. If any row is
// unchecked, every row becomes checked; if every row is already
// checked, the selection clears. Wired to the column-0 header click so
// users can mass-select before pressing Start/Stop/Delete.
//
// PublishRowsReset (fired transitively via onSelectionChanged →
// refreshAll) doesn't reliably re-push LVIS_STATEIMAGEMASK to already-
// rendered rows in walk's CheckBoxes mode — visible rows keep the
// stale checkbox visual until each one repaints on hover. Explicitly
// emitting RowsChanged(0, last) makes walk invalidate each item and
// re-query Checked() during the next paint, so every checkbox updates
// immediately.
func (m *ruleTableModel) ToggleAllSelected() {
	if len(m.rows) == 0 {
		return
	}
	target := len(m.SelectedSnapshots()) < len(m.rows)
	for _, r := range m.rows {
		if target {
			m.selected[r.Rule.ID] = true
		} else {
			delete(m.selected, r.Rule.ID)
		}
	}
	m.PublishRowsChanged(0, len(m.rows)-1)
	if m.onSelectionChanged != nil {
		m.onSelectionChanged()
	}
}

// snapshotByID looks up the cached snapshot for a rule ID. Used by the
// double-click handler to skip Edit on running rules.
func (m *ruleTableModel) snapshotByID(id string) (engineSnapshot, bool) {
	for _, r := range m.rows {
		if r.Rule.ID == id {
			return r, true
		}
	}
	return engineSnapshot{}, false
}

// rowID returns the rule ID at the given row, or "" if out of range.
// Used by the double-click activation handler.
func (m *ruleTableModel) rowID(row int) string {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	return m.rows[row].Rule.ID
}
