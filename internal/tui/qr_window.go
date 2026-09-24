package tui

import (
	"context"
	"errors"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"image"
	"image/color"
	"image/png"
	"os"
	process "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type qrWindowClosedMsg struct {
	gen int
	err error
}

func (m *Model) closeQRWindow() {
	if m.qrWindowCancel != nil {
		m.qrWindowCancel()
		m.qrWindowCancel = nil
	}
}
func (m *Model) ensureQRWindow() tea.Cmd {
	if !m.ready || m.session.phase != phaseWaiting || len(m.session.qrGrid) == 0 || m.qrWindowCancel != nil || m.qrWindowDone {
		return nil
	}
	qw, qh := qrRenderSize(len(m.session.qrGrid), qrQuietZone, m.cfg.ASCIIOnly)
	fits := false
	for _, slot := range m.panels {
		if slot.owner.zone == ZoneMain {
			fits = slot.interiorW >= qw && slot.interiorH >= qh+2
			break
		}
	}
	if fits && m.width >= minUsableWidth && m.height >= minUsableHeight {
		return nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.qrWindowCancel = cancel
	gen, grid := m.genSession, m.session.qrGrid
	launcher := m.qrWindowLauncher
	if launcher == nil {
		launcher = launchQRWindow
	}
	return func() tea.Msg { return qrWindowClosedMsg{gen: gen, err: launcher(ctx, grid)} }
}
func (m *Model) onQRWindowClosed(msg qrWindowClosedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.genSession {
		return m, nil
	}
	m.closeQRWindow()
	m.qrWindowDone = true
	if errors.Is(msg.err, context.Canceled) {
		return m, nil
	}
	if msg.err != nil {
		m.qrWindowErr = msg.err
		m.pushToast(sevWarn, "二维码独立窗口打开失败")
		return m, nil
	}
	if m.session.phase == phaseWaiting {
		return m.handleBack()
	}
	return m, nil
}

// Keep the credential local: the native viewer reads an owner-only temporary PNG.
// Removing that PNG closes the viewer, including when the login is cancelled.
func launchQRWindow(ctx context.Context, grid [][]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	powershell, err := process.LookPath("powershell.exe")
	if err != nil && runtime.GOOS == "linux" {
		powershell = "/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe"
		_, err = os.Stat(powershell)
	}
	if err != nil {
		return fmt.Errorf("当前环境没有可用的 Windows 二维码窗口，请放大终端")
	}
	dir, err := os.MkdirTemp("", "wfuseat-qr-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	imgPath := filepath.Join(dir, "qr.png")
	scriptPath := filepath.Join(dir, "viewer.ps1")
	if err = writeQRWindowPNG(imgPath, grid); err != nil {
		return err
	}
	if err = os.WriteFile(scriptPath, []byte(qrViewerScript), 0600); err != nil {
		return err
	}
	native := func(path string) (string, error) {
		if runtime.GOOS == "windows" {
			return path, nil
		}
		b, e := process.Command("wslpath", "-w", path).Output()
		return strings.TrimSpace(string(b)), e
	}
	script, err := native(scriptPath)
	if err != nil {
		return err
	}
	img, err := native(imgPath)
	if err != nil {
		return err
	}
	cmd := process.Command(powershell, "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-ExecutionPolicy", "Bypass", "-STA", "-File", script, "-ImagePath", img)
	if err = cmd.Start(); err != nil {
		return fmt.Errorf("无法启动二维码窗口：%w", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
		if err != nil {
			return fmt.Errorf("二维码窗口启动失败：%w", err)
		}
		return nil
	case <-ctx.Done():
		_ = os.Remove(imgPath)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		return ctx.Err()
	}
}
func writeQRWindowPNG(path string, grid [][]bool) error {
	const quiet = 4
	const scale = 12
	n := len(grid)
	if n == 0 {
		return fmt.Errorf("二维码为空")
	}
	size := (n + 2*quiet) * scale
	img := image.NewGray(image.Rect(0, 0, size, size))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for y, row := range grid {
		if len(row) != n {
			return fmt.Errorf("二维码尺寸无效")
		}
		for x, dark := range row {
			if dark {
				for dy := 0; dy < scale; dy++ {
					for dx := 0; dx < scale; dx++ {
						img.SetGray((x+quiet)*scale+dx, (y+quiet)*scale+dy, color.Gray{Y: 0})
					}
				}
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	err = png.Encode(f, img)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

const qrViewerScript = `param([string]$ImagePath)
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
[System.Windows.Forms.Application]::EnableVisualStyles()
if (!(Test-Path -LiteralPath $ImagePath)) { exit }
$bytes = [System.IO.File]::ReadAllBytes($ImagePath)
$stream = [System.IO.MemoryStream]::new($bytes, $false)
$loaded = [System.Drawing.Image]::FromStream($stream)
$bitmap = [System.Drawing.Bitmap]::new($loaded)
$loaded.Dispose(); $stream.Dispose()
$form = New-Object System.Windows.Forms.Form
$form.Text = 'WFU Seat - Scan QR Code'
$form.FormBorderStyle = 'None'
$form.StartPosition = 'Manual'
$form.WindowState = 'Normal'
$form.Bounds = [System.Windows.Forms.Screen]::FromPoint([System.Windows.Forms.Cursor]::Position).Bounds
$form.BackColor = [System.Drawing.Color]::White
$form.TopMost = $true
$form.KeyPreview = $true
$form.Add_KeyDown({ if ($_.KeyCode -eq 'Escape') { $form.Close() } })
$form.Add_Paint({
 $g = $_.Graphics
 $g.InterpolationMode = [System.Drawing.Drawing2D.InterpolationMode]::NearestNeighbor
 $g.PixelOffsetMode = [System.Drawing.Drawing2D.PixelOffsetMode]::Half
 $side = [int]([Math]::Min($form.ClientSize.Width, $form.ClientSize.Height))
 $x = [int](($form.ClientSize.Width - $side) / 2)
 $y = [int](($form.ClientSize.Height - $side) / 2)
 $g.DrawImage($bitmap, $x, $y, $side, $side)
})
$form.Add_Resize({ $form.Invalidate() })
$timer = New-Object System.Windows.Forms.Timer
$timer.Interval = 250
$timer.Add_Tick({ if (!(Test-Path -LiteralPath $ImagePath)) { $form.Close() } })
$timer.Start()
$form.Add_Shown({ $form.Activate() })
try { [System.Windows.Forms.Application]::Run($form) }
finally { $timer.Stop(); $timer.Dispose(); $bitmap.Dispose(); $form.Dispose() }
`
