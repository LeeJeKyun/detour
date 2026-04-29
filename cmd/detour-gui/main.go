//go:build windows

// Stage 3c: Start/Stop wired to runtime.Run via a runController. X button
// minimizes to the tray; Quit triggers cancel + a brief grace period before
// the process exits so WinDivert handles can drain.
package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"sync"
	"sync/atomic"
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
		countLb  *walk.Label
		statusLb *walk.Label
		ni       *walk.NotifyIcon
	)
	var ctrl runController

	// tickerStop guards the polling goroutine for the currently active rule.
	// It's recreated on each Start and closed in onDone to halt the goroutine.
	var tickerStop chan struct{}

	// cleanupTimedOut becomes true once onStop's fallback decided to force
	// exit. Kept as atomic so the X-button handler (GUI thread) and the
	// fallback goroutine can race safely.
	var cleanupTimedOut atomic.Bool

	// Tray icons: one for idle, one for active. Built at runtime from solid
	// color buffers — replace with .ico files or RT_GROUP_ICON for custom art.
	idleIcon := makeSolidIcon(color.RGBA{R: 0x88, G: 0x88, B: 0x88, A: 0xff})   // gray
	activeIcon := makeSolidIcon(color.RGBA{R: 0x3a, G: 0x9d, B: 0x6c, A: 0xff}) // green

	// setStatus / setRunning route widget mutation onto the GUI thread.
	// walk widgets are not goroutine-safe; Synchronize() is the official escape
	// hatch for callbacks fired from runtime.Run's worker goroutines.
	setStatus := func(s string) {
		if mw == nil {
			return
		}
		mw.Synchronize(func() { _ = statusLb.SetText(s) })
	}
	setCounts := func(fwd, rev uint64) {
		if mw == nil {
			return
		}
		mw.Synchronize(func() {
			_ = countLb.SetText(fmt.Sprintf("Forward: %d   Reverse: %d", fwd, rev))
			if ni != nil {
				_ = ni.SetToolTip(fmt.Sprintf("detour — fwd %d / rev %d", fwd, rev))
			}
		})
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
			// Tray icon swaps with running state so the user can tell at a
			// glance (gray = idle, green = active) without opening the window.
			if ni != nil {
				if running && activeIcon != nil {
					_ = ni.SetIcon(activeIcon)
				} else if !running && idleIcon != nil {
					_ = ni.SetIcon(idleIcon)
				}
			}
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
		setCounts(0, 0)

		// Atomic counters shared between runtime.Run (writer) and the
		// polling goroutine below (reader). New atomics each Start so the
		// counts visibly reset to 0 instead of carrying over.
		fwdCnt := &atomic.Uint64{}
		revCnt := &atomic.Uint64{}

		tickerStop = make(chan struct{})
		go func(stop <-chan struct{}, fwd, rev *atomic.Uint64) {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					setCounts(fwd.Load(), rev.Load())
				case <-stop:
					return
				}
			}
		}(tickerStop, fwdCnt, revCnt)

		started := ctrl.start(rule, runtime.Options{
			ForwardCounter: fwdCnt,
			ReverseCounter: revCnt,
			OnStop: func(s runtime.Stats) {
				setCounts(s.Forward, s.Reverse) // final value beats the next tick
				setStatus(fmt.Sprintf("Status: stopped (forward=%d reverse=%d)", s.Forward, s.Reverse))
			},
		}, func(err error) {
			if tickerStop != nil {
				close(tickerStop)
				tickerStop = nil
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				setStatus("Error: " + err.Error())
			}
			setRunning(false)
		})
		if !started {
			if tickerStop != nil {
				close(tickerStop)
				tickerStop = nil
			}
			setStatus("Status: already running")
		}
	}

	onStop := func() {
		setStatus("Status: stopping...")
		stopBtn.SetEnabled(false)
		ctrl.stop()

		// Fallback: if runtime.Run can't drain within 3s (rare WinDivert
		// quirk where Shutdown fails to wake a blocked Recv), force the
		// process to exit so the user is never trapped in "stopping..." with
		// no way to recover. Use os.Exit instead of walk.App().Exit because
		// the latter posts WM_QUIT to the message loop — if the loop is
		// wedged, the quit message never gets handled. os.Exit is OS-level
		// and always terminates the process.
		go func() {
			time.Sleep(3 * time.Second)
			if ctrl.isRunning() {
				cleanupTimedOut.Store(true)
				mw.Synchronize(func() {
					_ = statusLb.SetText("Status: cleanup timed out — exiting")
				})
				time.Sleep(300 * time.Millisecond)
				os.Exit(0)
			}
		}()
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
				AssignTo: &countLb,
				Text:     "Forward: 0   Reverse: 0",
			},
			Label{
				AssignTo: &statusLb,
				Text:     statusIdle,
			},
		},
	}).Create(); err != nil {
		log.Fatalf("create main window: %v", err)
	}

	// X button (and Alt+F4) policy:
	//   - rule actively running → hide to tray (rule keeps applying; tray
	//     icon is the only visible affordance)
	//   - idle (no rule running) → actually exit. Once the user has clicked
	//     Stop and the rule is down, X means "I'm done with this app."
	//   - cleanupTimedOut → exit immediately rather than wait the 300ms
	//     courtesy delay in onStop's fallback.
	//
	// walk's CloseEvent always fires with CloseReasonUnknown (WM_CLOSE in
	// form.go resets the reason before publish), so we can't tell X from
	// Alt+F4 from a programmatic close — same policy applies to all.
	//
	// firstHide forces a one-shot balloon the first time we hide so the user
	// knows the app is still alive in the tray. Subsequent hides are silent.
	var firstHide bool
	mw.Closing().Attach(func(canceled *bool, _ walk.CloseReason) {
		if cleanupTimedOut.Load() {
			// Cleanup is already wedged — bypass walk's message loop.
			os.Exit(0)
		}
		if !ctrl.isRunning() {
			// Idle: graceful walk shutdown is fine here.
			walk.App().Exit(0)
			return
		}
		*canceled = true
		mw.Hide()
		if !firstHide {
			firstHide = true
			if ni != nil {
				_ = ni.ShowInfo(
					"detour",
					"Still running in the system tray.\nLeft-click the icon to reopen, or right-click → Quit to exit.",
				)
			}
		}
	})

	mw.Show()
	validateForm()

	var niErr error
	ni, niErr = walk.NewNotifyIcon(mw)
	if niErr != nil {
		log.Fatalf("create notify icon: %v", niErr)
	}
	defer ni.Dispose()

	_ = ni.SetToolTip("detour")
	if idleIcon != nil {
		_ = ni.SetIcon(idleIcon)
	}

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

// makeSolidIcon builds a 16x16 single-color tray icon at runtime. Avoids the
// hassle of shipping .ico files; users wanting a custom design can replace
// the call sites with walk.NewIconFromFile / RT_GROUP_ICON later.
func makeSolidIcon(c color.Color) *walk.Icon {
	const size = 16
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, c)
		}
	}
	icon, err := walk.NewIconFromImage(img)
	if err != nil {
		// On failure fall back to no icon — the tray will draw a placeholder.
		return nil
	}
	return icon
}
