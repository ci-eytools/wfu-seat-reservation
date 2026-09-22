package tui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/skip2/go-qrcode"
)

func gridEqual(a, b [][]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

// The login endpoint only hands back a PNG, so the terminal rendering depends on
// recovering the exact module grid from pixels. This round-trips real encodings
// through the detector at several scales, with and without a quiet zone.
func TestDetectQRModulesRoundTrip(t *testing.T) {
	contents := []string{
		// The shape of the real payload: a uuid query string.
		"https://e.wfu.edu.cn/ssoApi/checkQRUUID?uuid=7285eeaa-8d55-424e-9776-1ef1d9449c59",
		"short",
		strings.Repeat("x", 300),
	}
	for _, content := range contents {
		for _, border := range []bool{false, true} {
			for _, scale := range []int{1, 2, 3, 4, 7} {
				// go-qrcode's encode() is not idempotent, so the reference grid
				// and the rendered image must come from separate instances.
				ref, err := qrcode.New(content, qrcode.Medium)
				if err != nil {
					t.Fatalf("qrcode.New: %v", err)
				}
				ref.DisableBorder = true
				want := ref.Bitmap()
				modules := len(want)

				img, err := qrcode.New(content, qrcode.Medium)
				if err != nil {
					t.Fatalf("qrcode.New: %v", err)
				}
				img.DisableBorder = !border
				pixelSize := modules
				if border {
					pixelSize = modules + 8
				}
				raw, err := img.PNG(scale * pixelSize)
				if err != nil {
					t.Fatalf("PNG: %v", err)
				}

				got, err := detectQRModules(raw)
				if err != nil {
					t.Fatalf("content len=%d border=%v scale=%d: %v", len(content), border, scale, err)
				}
				if !gridEqual(got, want) {
					t.Fatalf("content len=%d border=%v scale=%d: grid mismatch (%d vs %d modules)",
						len(content), border, scale, len(got), len(want))
				}
			}
		}
	}
}

func TestDetectQRModulesRejectsNonQR(t *testing.T) {
	solid := image.NewRGBA(image.Rect(0, 0, 120, 120))
	for y := 0; y < 120; y++ {
		for x := 0; x < 120; x++ {
			solid.Set(x, y, color.RGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, solid); err != nil {
		t.Fatal(err)
	}
	if _, err := detectQRModules(buf.Bytes()); err == nil {
		t.Fatal("a blank image must be rejected")
	}
	if _, err := detectQRModules([]byte("not a png")); err == nil {
		t.Fatal("a non-PNG must be rejected")
	}
}

func TestQRRenderSizeMatchesRender(t *testing.T) {
	const modules = 45
	for _, ascii := range []bool{false, true} {
		w, h := qrRenderSize(modules, qrQuietZone, ascii)
		grid := make([][]bool, modules)
		for i := range grid {
			grid[i] = make([]bool, modules)
		}
		out := renderQRText(grid, qrQuietZone, ascii)
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != h {
			t.Errorf("ascii=%v: rendered %d lines, size said %d", ascii, len(lines), h)
		}
		for _, line := range lines {
			if got := visibleWidth(line, ascii); got != w {
				t.Errorf("ascii=%v: line width %d, size said %d", ascii, got, w)
				break
			}
		}
	}
}

// visibleWidth counts terminal cells, ignoring ANSI escape sequences.
func visibleWidth(s string, ascii bool) int {
	if ascii {
		return len([]rune(s))
	}
	n := 0
	inEscape := false
	for _, r := range s {
		switch {
		case inEscape:
			if r == 'm' {
				inEscape = false
			}
		case r == 0x1b:
			inEscape = true
		default:
			n++
		}
	}
	return n
}
