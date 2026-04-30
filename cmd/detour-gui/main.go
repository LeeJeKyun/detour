//go:build windows

// Stage 4c: multi-rule GUI. The window is now a TableView of persisted
// rules; each row carries its own engine, packet counters, and check-box
// toggle. Rules persist to %APPDATA%/detour/rules.json automatically via
// the rules.Store wrapped by manager.
package main

import (
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"detour/internal/cli"
	"detour/internal/dnat"
	"detour/internal/rules"
)

var protoChoices = []string{"both", "tcp", "udp"}

func main() {
	storePath, err := rules.DefaultPath()
	if err != nil {
		log.Fatalf("locate config dir: %v", err)
	}
	store := rules.NewStore(storePath)
	if err := store.Load(); err != nil {
		// Bad config file shouldn't keep the user out of the app — log it
		// and start with an empty list. Once they Add a rule we'll
		// overwrite the broken file with a clean one.
		log.Printf("warning: failed to load %s: %v (starting empty)", storePath, err)
	}
	mgr := newManager(store)
	mgr.LoadFromStore()

	var (
		mw       *walk.MainWindow
		tv       *walk.TableView
		editBtn  *walk.PushButton
		delBtn   *walk.PushButton
		statusLb *walk.Label
		ni       *walk.NotifyIcon
	)
	var cleanupTimedOut atomic.Bool

	// Tray icons: built at runtime from solid color buffers. Replace with
	// real .ico art later if desired.
	idleIcon := makeSolidIcon(color.RGBA{R: 0x88, G: 0x88, B: 0x88, A: 0xff})
	activeIcon := makeSolidIcon(color.RGBA{R: 0x3a, G: 0x9d, B: 0x6c, A: 0xff})

	model := newRuleTableModel(mgr)

	// refreshAll snapshots manager state and pushes it into every widget
	// that depends on it: table model, status label, tray tooltip/icon,
	// edit/delete button enabled state. Always invoked on the GUI thread.
	refreshAll := func() {
		if mw == nil {
			return
		}
		model.refresh()
		model.PublishRowsReset()
		running, fwd, rev := mgr.AggregateCounts()
		_ = statusLb.SetText(fmt.Sprintf("Active: %d   Forward: %d   Reverse: %d", running, fwd, rev))
		if ni != nil {
			_ = ni.SetToolTip(fmt.Sprintf("detour — %d active, fwd %d / rev %d", running, fwd, rev))
			if running > 0 && activeIcon != nil {
				_ = ni.SetIcon(activeIcon)
			} else if idleIcon != nil {
				_ = ni.SetIcon(idleIcon)
			}
		}
		sel := tv.CurrentIndex()
		editBtn.SetEnabled(sel >= 0)
		delBtn.SetEnabled(sel >= 0)
	}

	syncRefresh := func() {
		if mw == nil {
			return
		}
		mw.Synchronize(refreshAll)
	}
	mgr.SetOnChanged(syncRefresh)

	model.onCheck = func(id string, checked bool) {
		var err error
		if checked {
			err = mgr.Start(id)
		} else {
			err = mgr.Stop(id)
		}
		if err != nil {
			walk.MsgBox(mw, "detour", err.Error(), walk.MsgBoxIconWarning)
		}
	}

	onAdd := func() {
		r, ok := openRuleDialog(mw, rules.Rule{Proto: dnat.ProtoBoth}, "Add rule")
		if !ok {
			return
		}
		if _, err := mgr.Add(r); err != nil {
			walk.MsgBox(mw, "detour — add failed", err.Error(), walk.MsgBoxIconError)
		}
	}
	onEdit := func() {
		id := model.rowID(tv.CurrentIndex())
		if id == "" {
			return
		}
		r, err := store.Get(id)
		if err != nil {
			return
		}
		edited, ok := openRuleDialog(mw, r, "Edit rule")
		if !ok {
			return
		}
		edited.ID = id
		if err := mgr.Update(edited); err != nil {
			walk.MsgBox(mw, "detour — edit failed", err.Error(), walk.MsgBoxIconError)
		}
	}
	onDelete := func() {
		id := model.rowID(tv.CurrentIndex())
		if id == "" {
			return
		}
		if walk.MsgBox(mw, "detour", "Delete this rule?", walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
			return
		}
		if err := mgr.Remove(id); err != nil {
			walk.MsgBox(mw, "detour — delete failed", err.Error(), walk.MsgBoxIconError)
		}
	}

	if err := (MainWindow{
		AssignTo: &mw,
		Title:    "detour",
		Size:     Size{Width: 760, Height: 360},
		Layout:   VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 12}, Spacing: 8},
		Children: []Widget{
			TableView{
				AssignTo:         &tv,
				AlternatingRowBG: true,
				CheckBoxes:       true,
				Columns: []TableViewColumn{
					{Title: "•", Width: 28, Alignment: AlignCenter},
					{Title: "From", Width: 150},
					{Title: "To", Width: 150},
					{Title: "Proto", Width: 60},
					{Title: "Forward", Width: 80, Alignment: AlignFar},
					{Title: "Reverse", Width: 80, Alignment: AlignFar},
					{Title: "Status", Width: 110},
				},
				Model: model,
				OnCurrentIndexChanged: func() {
					sel := tv.CurrentIndex()
					editBtn.SetEnabled(sel >= 0)
					delBtn.SetEnabled(sel >= 0)
				},
				OnItemActivated: onEdit,
			},
			Composite{
				Layout: HBox{Spacing: 8},
				Children: []Widget{
					PushButton{Text: "Add", OnClicked: onAdd},
					PushButton{AssignTo: &editBtn, Text: "Edit", Enabled: false, OnClicked: onEdit},
					PushButton{AssignTo: &delBtn, Text: "Delete", Enabled: false, OnClicked: onDelete},
					HSpacer{},
					Label{AssignTo: &statusLb, Text: "Active: 0   Forward: 0   Reverse: 0"},
				},
			},
		},
	}).Create(); err != nil {
		log.Fatalf("create main window: %v", err)
	}

	// X-button policy: any rule running → hide to tray (rules keep
	// running, the tray icon is the only visible affordance). Idle →
	// actually exit. cleanupTimedOut → bypass walk's message loop.
	var firstHide bool
	mw.Closing().Attach(func(canceled *bool, _ walk.CloseReason) {
		if cleanupTimedOut.Load() {
			os.Exit(0)
		}
		if !mgr.AnyRunning() {
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
		// Stop everything, then poll briefly until all engines drain.
		// Each engine has its own 1-second force-close inside runtime.Run,
		// so 3 seconds is plenty under normal conditions; if a wedged
		// handle pushes us past the deadline we force-exit so the user is
		// never stuck.
		mgr.StopAll()
		deadline := time.Now().Add(3 * time.Second)
		for mgr.AnyRunning() {
			if time.Now().After(deadline) {
				cleanupTimedOut.Store(true)
				os.Exit(0)
			}
			time.Sleep(50 * time.Millisecond)
		}
		walk.App().Exit(0)
	})
	_ = ni.ContextMenu().Actions().Add(quitAction)
	_ = ni.SetVisible(true)

	// 1s polling loop refreshes packet counts. Manager events already
	// handle add/remove/start/stop transitions immediately; this just
	// surfaces atomic counter ticks.
	pollStop := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				syncRefresh()
			case <-pollStop:
				return
			}
		}
	}()
	defer close(pollStop)

	refreshAll()
	mw.Run()
}

// openRuleDialog shows a modal form to capture/edit a rule. Returns the
// resulting Rule and true on OK; (zero, false) on Cancel or invalid input.
// initial seeds the fields — pass a zero Rule for Add.
func openRuleDialog(parent walk.Form, initial rules.Rule, title string) (rules.Rule, bool) {
	var (
		dlg       *walk.Dialog
		fromEdit  *walk.LineEdit
		toEdit    *walk.LineEdit
		protoCB   *walk.ComboBox
		okBtn     *walk.PushButton
		cancelBtn *walk.PushButton
		errLb     *walk.Label
	)
	initialFrom, initialTo := "", ""
	if initial.From.IP != nil {
		initialFrom = initial.From.String()
	}
	if initial.To.IP != nil {
		initialTo = initial.To.String()
	}
	initialProto := 0
	switch initial.Proto {
	case dnat.ProtoTCP:
		initialProto = 1
	case dnat.ProtoUDP:
		initialProto = 2
	}

	var resultRule rules.Rule
	var ok bool

	validate := func() {
		from := fromEdit.Text()
		to := toEdit.Text()
		_, errFrom := cli.ParseEndpoint(from)
		_, errTo := cli.ParseEndpoint(to)
		switch {
		case errFrom != nil && from != "":
			_ = errLb.SetText("From: " + errFrom.Error())
		case errTo != nil && to != "":
			_ = errLb.SetText("To: " + errTo.Error())
		default:
			_ = errLb.SetText("")
		}
		okBtn.SetEnabled(errFrom == nil && errTo == nil)
	}

	onOK := func() {
		from, errFrom := cli.ParseEndpoint(fromEdit.Text())
		to, errTo := cli.ParseEndpoint(toEdit.Text())
		if errFrom != nil || errTo != nil {
			return
		}
		proto := dnat.ProtoBoth
		switch protoCB.CurrentIndex() {
		case 1:
			proto = dnat.ProtoTCP
		case 2:
			proto = dnat.ProtoUDP
		}
		resultRule = rules.Rule{From: from, To: to, Proto: proto}
		ok = true
		dlg.Accept()
	}

	if err := (Dialog{
		AssignTo:      &dlg,
		Title:         title,
		DefaultButton: &okBtn,
		CancelButton:  &cancelBtn,
		MinSize:       Size{Width: 360, Height: 200},
		Layout:        VBox{Margins: Margins{Left: 12, Top: 12, Right: 12, Bottom: 12}, Spacing: 8},
		Children: []Widget{
			Composite{
				Layout: Grid{Columns: 2, Spacing: 8},
				Children: []Widget{
					Label{Text: "From (IP:Port):"},
					LineEdit{AssignTo: &fromEdit, Text: initialFrom, OnTextChanged: validate, CueBanner: "1.2.3.4:5000"},
					Label{Text: "To (IP:Port):"},
					LineEdit{AssignTo: &toEdit, Text: initialTo, OnTextChanged: validate, CueBanner: "127.0.0.1:5001"},
					Label{Text: "Protocol:"},
					ComboBox{AssignTo: &protoCB, Model: protoChoices, CurrentIndex: initialProto},
				},
			},
			Label{AssignTo: &errLb, Text: ""},
			Composite{
				Layout: HBox{Spacing: 8},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &okBtn, Text: "OK", OnClicked: onOK},
					PushButton{AssignTo: &cancelBtn, Text: "Cancel", OnClicked: func() { dlg.Cancel() }},
				},
			},
		},
	}).Create(parent); err != nil {
		return rules.Rule{}, false
	}
	validate()
	dlg.Run()
	return resultRule, ok
}

// makeSolidIcon builds a 16x16 single-color tray icon at runtime. Avoids
// the hassle of shipping .ico files; users wanting a custom design can
// replace the call sites with walk.NewIconFromFile / RT_GROUP_ICON.
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
		return nil
	}
	return icon
}
