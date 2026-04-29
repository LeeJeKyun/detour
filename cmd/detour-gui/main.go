//go:build windows

// Stage 3a skeleton: minimal main window + system-tray icon + Quit menu.
// Form widgets and Start/Stop integration land in 3b–3d.
package main

import (
	"log"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

func main() {
	var mw *walk.MainWindow

	if err := (MainWindow{
		AssignTo: &mw,
		Title:    "detour",
		Size:     Size{Width: 480, Height: 240},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "detour GUI — skeleton (Stage 3a)"},
			Label{Text: "Form / Start / Stop will be added in Stage 3b–3d."},
		},
	}).Create(); err != nil {
		log.Fatalf("create main window: %v", err)
	}

	// Tray icon. We don't have a custom .ico embedded yet, so fall back to
	// running without an icon — Windows will draw a generic placeholder.
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
