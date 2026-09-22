package tui

import (
	"context"
	"errors"
	tea "github.com/charmbracelet/bubbletea"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestQRCodeWindowOnlyWhenPanelCannotFitAndNoDuplicate(t *testing.T) {
	m := accountModel(t, t.TempDir(), "alice", &apiFake{})
	m.session.phase = phaseWaiting
	m.session.qrGrid = make([][]bool, 41)
	for i := range m.session.qrGrid {
		m.session.qrGrid[i] = make([]bool, 41)
	}
	launched := 0
	m.qrWindowLauncher = func(ctx context.Context, g [][]bool) error {
		launched++
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	m.Update(tea.WindowSizeMsg{Width: 220, Height: 90})
	if cmd := m.ensureQRWindow(); cmd != nil || m.qrWindowCancel != nil {
		t.Fatal("large panel opened window")
	}
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd == nil || m.qrWindowCancel == nil {
		t.Fatal("small panel failed to open window")
	}
	if m.ensureQRWindow() != nil {
		t.Fatal("duplicate window")
	}
	msg := cmd().(qrWindowClosedMsg)
	if launched != 1 || msg.err != nil {
		t.Fatal(launched, msg)
	}
	m.onQRWindowClosed(msg)
	if m.session.phase != phaseIdle || len(m.session.qrGrid) > 0 {
		t.Fatal("closing external window did not cancel login")
	}
}
func TestQRCodeWindowCancellationAndStaleResults(t *testing.T) {
	m := accountModel(t, t.TempDir(), "alice", &apiFake{})
	m.session.phase = phaseWaiting
	m.session.qrGrid = [][]bool{{true}}
	m.width = 20
	m.height = 10
	m.layout()
	var seen context.Context
	m.qrWindowLauncher = func(ctx context.Context, _ [][]bool) error { seen = ctx; return ctx.Err() }
	cmd := m.ensureQRWindow()
	if cmd == nil {
		t.Fatal("missing window command")
	}
	m.closeQRWindow()
	msg := cmd().(qrWindowClosedMsg)
	if seen == nil || !errors.Is(msg.err, context.Canceled) {
		t.Fatal(msg)
	}
	m.genSession++
	m.onQRWindowClosed(msg)
	if m.qrWindowDone {
		t.Fatal("old result changed new login")
	}
}
func TestQRCodePNGHasQuietZoneAndPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qr.png")
	if e := writeQRWindowPNG(path, [][]bool{{true, false}, {false, true}}); e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	img, e := png.Decode(f)
	if e != nil {
		t.Fatal(e)
	}
	white, _, _, _ := img.At(0, 0).RGBA()
	black, _, _, _ := img.At(48, 48).RGBA()
	if white != 65535 || black != 0 || img.Bounds().Dx() != 120 {
		t.Fatal("invalid module rendering")
	}
	st, _ := f.Stat()
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
}
