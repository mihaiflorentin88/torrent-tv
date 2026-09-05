//go:build !headless && !(linux && arm)

package gui

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"
)

// wantItem is a compact trayItem matcher for table tests.
type wantItem struct {
	label       string
	action      trayAction
	checkbox    bool
	checked     bool
	disabled    bool
	wantTooltip bool
}

func model(t *testing.T, s State, on bool, err error) []wantItem {
	t.Helper()
	items := trayMenuModel(s, on, err)
	if len(items) != 5 {
		t.Fatalf("menu model has %d items, want 5: %+v", len(items), items)
	}
	out := make([]wantItem, len(items))
	for i, it := range items {
		out[i] = wantItem{
			label:       it.Label,
			action:      it.Action,
			checkbox:    it.Checkbox,
			checked:     it.Checked,
			disabled:    it.Disabled,
			wantTooltip: it.Tooltip != "",
		}
	}
	return out
}

func compare(t *testing.T, got []wantItem, want []wantItem) {
	t.Helper()
	for i, w := range want {
		g := got[i]
		if g != w {
			t.Errorf("item %d = %+v, want %+v", i, g, w)
		}
	}
}

// TestTrayMenuModelStates covers every state × autostart combination: the
// labels, the transitioning disable, and the checkbox mirror of the
// read-back.
func TestTrayMenuModelStates(t *testing.T) {
	states := []State{StateStopped, StateStarting, StateRunning, StateStopping, StateFailed}
	for _, s := range states {
		for _, on := range []bool{false, true} {
			got := model(t, s, on, nil)
			want := []wantItem{
				{label: "Open Torrent TV", action: trayActionOpen},
				{label: "Open web UI", action: trayActionOpenWebUI},
				{label: "Start at login", action: trayActionToggleAutostart, checkbox: true, checked: on},
				{label: "Quit", action: trayActionQuit},
			}
			toggle := wantItem{
				action:   trayActionToggleServer,
				disabled: s == StateStarting || s == StateStopping,
			}
			if s == StateRunning {
				toggle.label = "Stop server"
			} else {
				toggle.label = "Start server"
			}
			// Model order: Open, toggle, web UI, autostart, Quit.
			ordered := []wantItem{want[0], toggle, want[1], want[2], want[3]}
			compare(t, got, ordered)
		}
	}
}

// TestTrayMenuModelAutostartError pins that a failed Enabled() read-back is
// never presented as "off" without explanation: unchecked plus a tooltip.
func TestTrayMenuModelAutostartError(t *testing.T) {
	items := trayMenuModel(StateStopped, true, errors.New("launchd read failed"))
	cb := items[3]
	if cb.Checked {
		t.Error("autostart checkbox checked despite read error")
	}
	if cb.Tooltip == "" {
		t.Error("autostart checkbox has no tooltip explaining the read error")
	}
	if cb.Tooltip == "Start at login" || cb.Label != "Start at login" {
		t.Errorf("unexpected autostart item %+v", cb)
	}
}

// TestTrayIconFor pins the state→embedded-PNG mapping and that every
// embedded icon decodes as a PNG at the expected size.
func TestTrayIconFor(t *testing.T) {
	cases := map[State]string{
		StateRunning:   "running",
		StateStopped:   "stopped",
		StateStarting:  "stopped",
		StateStopping:  "stopped",
		StateFailed:    "failed",
		State("bogus"): "stopped",
	}
	for s, want := range cases {
		icon := trayIconFor(s)
		if icon == nil {
			t.Fatalf("trayIconFor(%q) returned nil", s)
		}
		wantBytes, err := TrayIcons.ReadFile(fmt.Sprintf("assets/tray/tray-%s-%d.png", want, trayIconSize))
		if err != nil {
			t.Fatalf("expected icon tray-%s missing: %v", want, err)
		}
		if !bytes.Equal(icon, wantBytes) {
			t.Errorf("trayIconFor(%q) does not match tray-%s-%d.png", s, want, trayIconSize)
		}
	}
}

// TestTrayIconStatesDistinct proves the three masters really differ:
// grayscale applied to stopped/failed, and the failed red dot present.
func TestTrayIconStatesDistinct(t *testing.T) {
	decode := func(name string) image.Image {
		data, err := TrayIcons.ReadFile("assets/tray/tray-" + name + "-64.png")
		if err != nil {
			t.Fatalf("read tray-%s: %v", name, err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("decode tray-%s: %v", name, err)
		}
		return img
	}
	running := decode("running")
	stopped := decode("stopped")
	failed := decode("failed")

	if running.At(32, 32) == stopped.At(32, 32) {
		t.Error("running and stopped identical at center; grayscale missing")
	}
	// The red dot sits at the bottom-right corner of the failed master;
	// LANCZOS resampling blends the edge, so assert "clearly red" rather
	// than the exact #e5484d.
	dot := color.RGBAModel.Convert(failed.At(60, 60)).(color.RGBA)
	if dot.R < 200 || dot.G > 130 || dot.B > 140 || dot.A < 200 {
		t.Errorf("failed dot color %v, want a red close to #e5484d", dot)
	}
	if stopped.At(60, 60) == failed.At(60, 60) {
		t.Error("failed icon indistinguishable from stopped at dot position")
	}
}

// TestRunActionDisabledToggleIsInert pins the enforced half of the
// "disabled while transitioning" guarantee: a click whose model snapshot
// was disabled must not start or stop the server, however the supervisor
// has moved on since the menu was built.
func TestRunActionDisabledToggleIsInert(t *testing.T) {
	sup := newTestSupervisor(t, &fakeApp{}, nil, nil)
	tctrl := &trayController{sup: sup}
	tctrl.runAction(trayItem{Label: "Start server", Action: trayActionToggleServer, Disabled: true})
	if got := sup.State(); got != StateStopped {
		t.Errorf("disabled toggle changed state to %q, want stopped", got)
	}
}

// TestRunActionEnabledToggleStarts pins the other half: an enabled
// snapshot toggles the server (stopped → start here).
func TestRunActionEnabledToggleStarts(t *testing.T) {
	serve := make(chan error)
	sup := newTestSupervisor(t, &fakeApp{serve: serve, closed: make(chan struct{})}, nil, nil)
	tctrl := &trayController{sup: sup}
	tctrl.runAction(trayItem{Label: "Start server", Action: trayActionToggleServer})
	if got := sup.State(); got != StateStarting {
		t.Fatalf("enabled toggle left state %q, want starting", got)
	}
	// Let the serve goroutine finish and shut the supervisor down cleanly.
	close(serve)
	deadline := time.Now().Add(5 * time.Second)
	for sup.State() != StateRunning && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sup.State() != StateRunning {
		t.Fatalf("server never reached running, stuck at %q", sup.State())
	}
	if err := sup.Stop(); err != nil {
		t.Fatalf("cleanup Stop: %v", err)
	}
}
