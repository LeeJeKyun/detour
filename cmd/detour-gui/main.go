//go:build windows

// Stage 3b: input form + live validation. Start/Stop wiring lands in 3c.
package main

import (
	"log"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"detour/internal/cli"
)

const (
	statusReady = "Status: ready"
	statusIdle  = "Status: enter From and To to enable Start"
)

var protoChoices = []string{"both", "tcp", "udp"}

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

	// validateForm runs cli.ParseEndpoint on each field on every keystroke.
	// Start is enabled only when both endpoints parse cleanly. The first error
	// (From > To order) surfaces in the status label; the second clears once
	// the first is fixed.
	validateForm := func() {
		if fromEdit == nil || toEdit == nil || statusLb == nil || startBtn == nil {
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
						AssignTo: &startBtn,
						Text:     "Start",
						Enabled:  false,
						OnClicked: func() {
							// Stage 3c will replace this with runtime.Run.
							_ = statusLb.SetText("Status: (start handler not wired yet — Stage 3c)")
							startBtn.SetEnabled(false)
							stopBtn.SetEnabled(true)
						},
					},
					PushButton{
						AssignTo: &stopBtn,
						Text:     "Stop",
						Enabled:  false,
						OnClicked: func() {
							_ = statusLb.SetText(statusReady)
							stopBtn.SetEnabled(false)
							validateForm()
						},
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

	mw.Show()
	validateForm()

	ni, err := walk.NewNotifyIcon(mw)
	if err != nil {
		log.Fatalf("create notify icon: %v", err)
	}
	defer ni.Dispose()

	_ = ni.SetToolTip("detour")

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
		walk.App().Exit(0)
	})
	_ = ni.ContextMenu().Actions().Add(quitAction)

	_ = ni.SetVisible(true)

	mw.Run()
}

// chosenProto reads the current selection from the protocol combo box and
// returns the parsed dnat.Protocol. Reserved for Stage 3c wiring.
func chosenProto(cb *walk.ComboBox) string {
	idx := cb.CurrentIndex()
	if idx < 0 || idx >= len(protoChoices) {
		return "both"
	}
	return protoChoices[idx]
}
