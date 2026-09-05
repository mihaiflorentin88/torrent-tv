//go:build !headless && !(linux && arm)

package gui

import (
	"fmt"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// trayIconSize is the embedded PNG size handed to SetIcon; the OS scales it
// to the menubar/tray. 32 px keeps small sizes crisp without upsampling.
const trayIconSize = 32

// trayIconFor returns the embedded PNG bytes for the given lifecycle state,
// falling back to the stopped icon for unknown states.
func trayIconFor(s State) []byte {
	name := "stopped"
	switch s {
	case StateRunning:
		name = "running"
	case StateFailed:
		name = "failed"
	}
	icon, err := TrayIcons.ReadFile(fmt.Sprintf("assets/tray/tray-%s-%d.png", name, trayIconSize))
	if err != nil {
		// The embed is committed; a miss is a programming error, but the
		// tray must never crash the app over it.
		return nil
	}
	return icon
}

// trayAction identifies what a tray menu item does when clicked.
type trayAction int

const (
	trayActionOpen trayAction = iota
	trayActionToggleServer
	trayActionOpenWebUI
	trayActionToggleAutostart
	trayActionQuit
)

// trayItem is one row of the tray menu model: a pure description of the
// menu that trayMenuModel derives from the server state and the autostart
// read-back. Building the wails menu from it is mechanical, which keeps
// every label/checkbox/decision unit-testable without a display.
type trayItem struct {
	Label    string
	Action   trayAction
	Checkbox bool
	Checked  bool
	Disabled bool
	Tooltip  string
}

// trayMenuModel derives the full tray menu for a state. autostartErr is the
// error from the Enabled() read-back: the checkbox then shows unchecked and
// carries a tooltip explaining why, so a failed read is never silently
// presented as "off".
func trayMenuModel(s State, autostartEnabled bool, autostartErr error) []trayItem {
	items := []trayItem{{Label: "Open Torrent TV", Action: trayActionOpen}}

	toggle := trayItem{
		Label:    "Start server",
		Action:   trayActionToggleServer,
		Disabled: s == StateStarting || s == StateStopping,
	}
	if s == StateRunning {
		toggle.Label = "Stop server"
	}
	items = append(items, toggle, trayItem{Label: "Open web UI", Action: trayActionOpenWebUI})

	autostartItem := trayItem{
		Label:    "Start at login",
		Action:   trayActionToggleAutostart,
		Checkbox: true,
		Checked:  autostartEnabled && autostartErr == nil,
	}
	if autostartErr != nil {
		autostartItem.Tooltip = "Autostart status unavailable: " + autostartErr.Error()
	}
	items = append(items, autostartItem, trayItem{Label: "Quit", Action: trayActionQuit})
	return items
}

// trayController owns the application SystemTray: state icon, rebuilt menu,
// and the autostart checkbox. Refresh is safe from any goroutine — see its
// comment for the main-thread story.
type trayController struct {
	app  *application.App
	win  application.Window
	sup  *Supervisor
	bind *Bindings
	tray *application.SystemTray
}

// newTray creates the system tray, installs the click-to-show handler and
// the initial menu, and returns the controller the runner drives.
func newTray(app *application.App, win application.Window, sup *Supervisor, bind *Bindings) *trayController {
	tray := app.SystemTray.New()
	tray.SetIcon(trayIconFor(sup.State()))
	tray.OnClick(func() { win.Show() }) // Windows/Linux; macOS uses the menu
	t := &trayController{app: app, win: win, sup: sup, bind: bind, tray: tray}
	t.Refresh(sup.State())
	tray.Show()
	return t
}

// Refresh swaps the state icon and rebuilds the whole menu (SetMenu, no
// partial updates).
//
// Main-thread story, verified against the pinned wails v3.0.0-beta.16
// source: SystemTray.SetIcon and SystemTray.SetMenu already hop to the main
// thread internally via InvokeSync (systemtray.go:231/253), and beta.16 has
// no App.RunOnMain — the brief's t.app.RunOnMain sketch does not exist in
// the pinned API. Menu construction itself is plain Go struct writes, safe
// off-thread. The Enabled() read-back runs here, off the main thread, so
// only the two already-safe setters touch the OS.
func (t *trayController) Refresh(s State) {
	autostartOn, autostartErr := t.bind.AutostartStatus()
	model := trayMenuModel(s, autostartOn, autostartErr)
	t.tray.SetIcon(trayIconFor(s))
	t.tray.SetMenu(t.buildMenu(model))
}

// buildMenu materializes the model into a wails menu. Every click handler
// captures its model item and dispatches through runAction, so a disabled
// row stays inert no matter when the click lands.
func (t *trayController) buildMenu(model []trayItem) *application.Menu {
	menu := application.NewMenu()
	for _, item := range model {
		var mi *application.MenuItem
		if item.Checkbox {
			mi = application.NewMenuItemCheckbox(item.Label, item.Checked)
			menu.Append(application.NewMenuFromItems(mi))
		} else {
			mi = menu.Add(item.Label)
		}
		mi.SetEnabled(!item.Disabled)
		mi.SetTooltip(item.Tooltip)
		mi.OnClick(func(*application.Context) { t.runAction(item) })
	}
	return menu
}

// runAction executes one menu item's action against the model snapshot it
// was built from. The disabled check is the enforced half of the "no
// transitions" guarantee: a click queued while the server was stopping
// must not silently start it.
func (t *trayController) runAction(item trayItem) {
	switch item.Action {
	case trayActionOpen:
		t.win.Show()
	case trayActionToggleServer:
		if item.Disabled {
			return
		}
		if t.sup.State() == StateRunning {
			_ = t.sup.Stop()
		} else {
			_ = t.sup.Start()
		}
	case trayActionOpenWebUI:
		_ = t.bind.OpenWebUI()
	case trayActionToggleAutostart:
		on, err := t.bind.AutostartStatus()
		if err == nil && on {
			_ = t.bind.DisableAutostart()
		} else {
			_ = t.bind.EnableAutostart()
		}
		t.Refresh(t.sup.State()) // reflect the Enable/Disable read-back
	case trayActionQuit:
		t.app.Quit()
	}
}
