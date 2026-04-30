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
// row-level Start/Stop toggle.
type ruleTableModel struct {
	walk.TableModelBase
	mgr  *manager
	rows []engineSnapshot

	// onCheck is invoked when the user toggles an Active checkbox. Wired
	// from main.go to manager.Start / manager.Stop on the GUI thread.
	onCheck func(id string, checked bool)
}

func newRuleTableModel(mgr *manager) *ruleTableModel {
	m := &ruleTableModel{mgr: mgr}
	m.refresh()
	return m
}

// refresh re-reads from the manager. Call from the GUI thread before
// PublishRowsReset so the table picks up the new snapshot.
func (m *ruleTableModel) refresh() {
	m.rows = m.mgr.SnapshotAll()
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

// Checked / SetChecked satisfy walk.ItemChecker. Toggling the box on a
// row hands the rule ID to onCheck so the manager can start/stop the
// engine; the model itself does not mutate state directly.
func (m *ruleTableModel) Checked(index int) bool {
	if index < 0 || index >= len(m.rows) {
		return false
	}
	s := m.rows[index].State
	return s == engineRunning || s == engineStopping
}

func (m *ruleTableModel) SetChecked(index int, checked bool) error {
	if index < 0 || index >= len(m.rows) {
		return nil
	}
	if m.onCheck == nil {
		return nil
	}
	id := m.rows[index].Rule.ID
	m.onCheck(id, checked)
	return nil
}

// rowID returns the rule ID at the given row, or "" if out of range.
// Callers (Edit/Delete buttons) use this to map a selected row back to
// the manager.
func (m *ruleTableModel) rowID(row int) string {
	if row < 0 || row >= len(m.rows) {
		return ""
	}
	return m.rows[row].Rule.ID
}
