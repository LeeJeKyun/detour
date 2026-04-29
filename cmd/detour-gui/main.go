//go:build windows

// Stage 3c: Start/Stop wired to runtime.Run via a runController. X button
// minimizes to the tray; Quit triggers cancel + a brief grace period before
// the process exits so WinDivert handles can drain.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"detour/internal/cli"
	"detour/internal/dnat"
	"detour/internal/runtime"
)

const (
	statusReady = "Status: ready"
	statusIdle  = "Status: enter From and To to enable Start"
)

var protoChoices = []string{"both", "tcp", "udp"}

// runController owns the lifetime of a single runtime.Run invocation. start()
// is non-blocking — it kicks off a goroutine and returns immediately. stop()
// cancels the in-flight context; the actual teardown happens on the goroutine.
type runController struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
}

func (c *runController) start(rule runtime.Rule, opts runtime.Options, onDone func(error)) bool {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.running = true
	c.mu.Unlock()

	go func() {
		err := runtime.Run(ctx, rule, opts)
		c.mu.Lock()
		c.running = false
		c.cancel = nil
		c.mu.Unlock()
		if onDone != nil {
			onDone(err)
		}
	}()
	return true
}

func (c *runController) stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *runController) isRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

func main() {
	var (
		mw       *walk.MainWindow
		fromEdit *walk.LineEdit
		toEdit   *walk.LineEdit
		protoCB  *walk.ComboBox
		startBtn *walk.PushButton
		stopBtn  *walk.PushButton
		statusLb *walk.Label
	)
	var ctrl runController

	// setStatus / setRunning route widget mutation onto the GUI thread.
	// walk widgets are not goroutine-safe; Synchronize() is the official escape
	// hatch for callbacks fired from runtime.Run's worker goroutines.
	setStatus := func(s string) {
		if mw == nil {
			return
		}
		mw.Synchronize(func() { _ = statusLb.SetText(s) })
	}
	setRunning := func(running bool) {
		if mw == nil {
			return
		}
		mw.Synchronize(func() {
			fromEdit.SetEnabled(!running)
			toEdit.SetEnabled(!running)
			protoCB.SetEnabled(!running)
			stopBtn.SetEnabled(running)
			if running {
				startBtn.SetEnabled(false)
				return
			}
			// idle: re-evaluate Start based on current input validity
			_, errFrom := cli.ParseEndpoint(fromEdit.Text())
			_, errTo := cli.ParseEndpoint(toEdit.Text())
			startBtn.SetEnabled(errFrom == nil && errTo == nil)
		})
	}

	// Live validation runs on every keystroke, but only when no rule is active.
	validateForm := func() {
		if fromEdit == nil || toEdit == nil || statusLb == nil || startBtn == nil {
			return
		}
		if ctrl.isRunning() {
			return
		}
		_, errFrom := cli.ParseEndpoint(fromEdit.Text())
		_, errTo := cli.ParseEndpoint(toEdit.Text())
		switch {
		case errFrom != nil && fromEdit.Text() != "":
			_ = statusLb.SetText("From: " + errFrom.Error())
		case errTo != nil && toEdit.Text() != "":
			_ = statusLb.SetText("To: " + errTo.Error())
		case errFrom == nil && errTo == nil:
			_ = statusLb.SetText(statusReady)
		default:
			_ = statusLb.SetText(statusIdle)
		}
		startBtn.SetEnabled(errFrom == nil && errTo == nil)
	}

	onStart := func() {
		from, errFrom := cli.ParseEndpoint(fromEdit.Text())
		to, errTo := cli.ParseEndpoint(toEdit.Text())
		if errFrom != nil || errTo != nil {
			return // shouldn't happen — Start is disabled until both validate
		}
		rule := runtime.Rule{From: from, To: to, Proto: chosenProto(protoCB)}

		setRunning(true)
		setStatus("Status: running — " + rule.String())

		started := ctrl.start(rule, runtime.Options{
			OnStop: func(s runtime.Stats) {
				setStatus(fmt.Sprintf("Status: stopped (forward=%d reverse=%d)", s.Forward, s.Reverse))
			},
		}, func(err error) {
			if err != nil && !errors.Is(err, context.Canceled) {
				setStatus("Error: " + err.Error())
			}
			setRunning(false)
		})
		if !started {
			setStatus("Status: already running")
		}
	}

	onStop := func() {
		setStatus("Status: stopping...")
		stopBtn.SetEnabled(false)
		ctrl.stop()
	}

	if err := (MainWindow{
		AssignTo: &mw,
		Title:    "detour",
		Size:     Size{Width: 480, Height: 280},
		Layout:   VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 12}, Spacing: 8},
		Children: []Widget{
			Composite{
				Layout: Grid{Columns: 2, Spacing: 8},
				Children: []Widget{
					Label{Text: "From (IP:Port):"},
					LineEdit{
						AssignTo:      &fromEdit,
						CueBanner:     "1.2.3.4:5000",
						OnTextChanged: validateForm,
					},

					Label{Text: "To (IP:Port):"},
					LineEdit{
						AssignTo:      &toEdit,
						CueBanner:     "127.0.0.1:5001",
						OnTextChanged: validateForm,
					},

					Label{Text: "Protocol:"},
					ComboBox{
						AssignTo:     &protoCB,
						Model:        protoChoices,
						CurrentIndex: 0,
					},
				},
			},

			Composite{
				Layout: HBox{Spacing: 8},
				Children: []Widget{
					PushButton{
						AssignTo:  &startBtn,
						Text:      "Start",
						Enabled:   false,
						OnClicked: onStart,
					},
					PushButton{
						AssignTo:  &stopBtn,
						Text:      "Stop",
						Enabled:   false,
						OnClicked: onStop,
					},
					HSpacer{},
				},
			},

			Label{
				AssignTo: &statusLb,
				Text:     statusIdle,
			},
		},
	}).Create(); err != nil {
		log.Fatalf("create main window: %v", err)
	}

	// X button: hide to tray instead of exiting. The tray's Quit action is
	// the only path that actually terminates the process.
	mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if reason == walk.CloseReasonUser {
			*canceled = true
			mw.Hide()
		}
	})

	mw.Show()
	validateForm()

	ni, err := walk.NewNotifyIcon(mw)
	if err != nil {
		log.Fatalf("create notify icon: %v", err)
	}
	defer ni.Dispose()

	_ = ni.SetToolTip("detour")

	// Left-click on the tray icon brings the main window back.
	ni.MouseDown().Attach(func(_, _ int, button walk.MouseButton) {
		if button == walk.LeftButton {
			mw.Show()
			_ = mw.SetFocus()
		}
	})

	openAction := walk.NewAction()
	_ = openAction.SetText("Open")
	openAction.Triggered().Attach(func() {
		mw.Show()
		_ = mw.SetFocus()
	})
	_ = ni.ContextMenu().Actions().Add(openAction)

	quitAction := walk.NewAction()
	_ = quitAction.SetText("Quit")
	quitAction.Triggered().Attach(func() {
		// Cancel the running rule, then give WinDivert a moment to release
		// its driver handles before the process exits. 500ms is in line with
		// the CLI's 3s ceiling but tuned for snappier GUI shutdown.
		ctrl.stop()
		if ctrl.isRunning() {
			time.Sleep(500 * time.Millisecond)
		}
		walk.App().Exit(0)
	})
	_ = ni.ContextMenu().Actions().Add(quitAction)

	_ = ni.SetVisible(true)

	mw.Run()
}

// chosenProto turns the protocol combo box selection into a dnat.Protocol.
func chosenProto(cb *walk.ComboBox) dnat.Protocol {
	switch cb.CurrentIndex() {
	case 1:
		return dnat.ProtoTCP
	case 2:
		return dnat.ProtoUDP
	default:
		return dnat.ProtoBoth
	}
}
