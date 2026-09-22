package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"wfuseat/internal/config"
)

// TestLiveQRStart hits the real login entry point. It is opt-in because it needs
// network access and generates a real (short-lived) login code.
//
//	WFUSEAT_LIVE=1 go test ./internal/tui -run TestLiveQRStart -v
func TestLiveQRStart(t *testing.T) {
	if os.Getenv("WFUSEAT_LIVE") == "" {
		t.Skip("set WFUSEAT_LIVE=1 to contact the real service")
	}
	cfg := config.Default()
	// Use the shipped default (the working local proxy); set WFUSEAT_LIVE_PROXY=
	// to force a direct connection instead.
	if proxy, ok := os.LookupEnv("WFUSEAT_LIVE_PROXY"); ok {
		cfg.Proxy = proxy
	}
	cfg.LogDir = t.TempDir()

	m, err := New(cfg, "/tmp/wfuseat-live-config.json")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer m.Close()
	m.Update(tea.WindowSizeMsg{Width: 130, Height: 45})

	startMsgs := exec(t, feed(t, m, keyMsg('r')))
	t.Logf("start produced %d message(s)", len(startMsgs))
	pollCmd := feedAll(t, m, startMsgs)

	n := len(m.session.qrGrid)
	t.Logf("phase=%d busy=%v qrModules=%d err=%v", m.session.phase, m.session.busy, n, m.session.err)
	if m.session.err != nil {
		t.Logf("err detail: %s", m.session.err.Detail)
	}

	qw, qh := qrRenderSize(n, qrQuietZone, cfg.ASCIIOnly)
	w, h := m.frameSize()
	t.Logf("qr footprint=%dx%d content=%dx%d", qw, qh, w, h)

	view := m.View()
	t.Logf("view contains 剩余=%v 尺寸不足=%v 尚未登录=%v 获取二维码=%v",
		strings.Contains(view, "剩余"),
		strings.Contains(view, "终端尺寸不足"),
		strings.Contains(view, "尚未登录"),
		strings.Contains(view, "获取二维码"))
	t.Logf("view has half-block cells=%v", strings.Contains(view, "▀"))

	if n == 0 {
		t.Fatal("no QR modules were recovered from the live service")
	}
	if !validQRModuleCount(n) {
		t.Fatalf("recovered %d modules, not a valid QR size", n)
	}
	if !strings.Contains(view, "▀") {
		t.Errorf("the QR was not rendered; the live flow needs investigation")
	}

	// Exercise one real poll without waiting for a phone.
	exec(t, pollCmd)
	t.Logf("after one poll: phase=%d polls=%d err=%v", m.session.phase, m.session.polls, m.session.err)
}
