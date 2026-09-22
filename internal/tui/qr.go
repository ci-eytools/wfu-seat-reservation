package tui

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// qrQuietZone is how many light modules are added around a detected code. The
// server-rendered image carries none, but scanners require a margin, so the
// terminal rendering must add one for the code to be readable at all.
const qrQuietZone = 4

// The 7x7 finder pattern shared by the three corners of every QR code.
var finderPattern = [7][7]bool{
	{true, true, true, true, true, true, true},
	{true, false, false, false, false, false, true},
	{true, false, true, true, true, false, true},
	{true, false, true, true, true, false, true},
	{true, false, true, true, true, false, true},
	{true, false, false, false, false, false, true},
	{true, true, true, true, true, true, true},
}

// detectQRModules recovers the module grid from a server-rendered QR PNG.
//
// The login endpoint returns the QR only as an image, so the grid has to be
// recovered from pixels. The render is clean and axis-aligned, so the approach
// is: binarise, take the dark bounding box, then accept the QR version whose
// sampled grid satisfies the finder and timing patterns. This needs no QR
// decoder dependency and makes no assumption about the encoded payload.
//
// The returned grid is only ever rendered. The payload is a live login
// credential and is never logged, displayed, or written to disk.
func detectQRModules(pngBytes []byte) ([][]bool, error) {
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return nil, fmt.Errorf("二维码图片无法解码: %w", err)
	}
	return detectModulesFromImage(img)
}

func detectModulesFromImage(img image.Image) ([][]bool, error) {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w < 21 || h < 21 {
		return nil, errors.New("二维码图片尺寸过小")
	}

	dark := func(x, y int) bool {
		r, g, bl, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
		luma := (299*r + 587*g + 114*bl) / 1000
		return luma < 0x8000
	}

	// The three finder patterns are dark at the corners, so the dark bounding
	// box is exactly the symbol, with or without a surrounding quiet zone.
	x0, y0, x1, y1 := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if !dark(x, y) {
				continue
			}
			if x < x0 {
				x0 = x
			}
			if x > x1 {
				x1 = x
			}
			if y < y0 {
				y0 = y
			}
			if y > y1 {
				y1 = y
			}
		}
	}
	if x1 < 0 {
		return nil, errors.New("二维码图片没有任何内容")
	}
	bw, bh := x1-x0+1, y1-y0+1
	if bw < 21 || bh < 21 {
		return nil, errors.New("二维码区域过小")
	}
	if diff := bw - bh; diff > bw/10 || diff < -(bw/10) {
		return nil, errors.New("二维码区域不是正方形")
	}

	sample := func(n int) [][]bool {
		grid := make([][]bool, n)
		for j := range grid {
			grid[j] = make([]bool, n)
			sy := y0 + int((float64(j)+0.5)*float64(bh)/float64(n))
			if sy > y1 {
				sy = y1
			}
			for i := range grid[j] {
				sx := x0 + int((float64(i)+0.5)*float64(bw)/float64(n))
				if sx > x1 {
					sx = x1
				}
				grid[j][i] = dark(sx, sy)
			}
		}
		return grid
	}

	for version := 0; version <= 40; version++ {
		n := 17 + 4*version
		if n > bw || n > bh {
			break
		}
		grid := sample(n)
		if validQRGrid(grid, n) {
			return grid, nil
		}
	}
	return nil, errors.New("无法从图片中识别出二维码网格")
}

// validQRGrid checks the structures that are present in every QR symbol, which
// is enough to reject a wrong sampling resolution with very high confidence.
func validQRGrid(grid [][]bool, n int) bool {
	if n < 21 || len(grid) != n {
		return false
	}
	matchesFinder := func(oy, ox int) bool {
		for i := 0; i < 7; i++ {
			for j := 0; j < 7; j++ {
				if grid[oy+i][ox+j] != finderPattern[i][j] {
					return false
				}
			}
		}
		return true
	}
	if !matchesFinder(0, 0) || !matchesFinder(0, n-7) || !matchesFinder(n-7, 0) {
		return false
	}
	// Timing patterns alternate along row 6 and column 6.
	for k := 8; k < n-8; k++ {
		want := k%2 == 0
		if grid[6][k] != want || grid[k][6] != want {
			return false
		}
	}
	// The dark module sits at (4*version+9, 8) and is always dark.
	if !grid[n-8][8] {
		return false
	}
	return true
}

// qrRenderSize reports the terminal cell footprint of a rendered n-module code.
func qrRenderSize(modules, quiet int, ascii bool) (width, height int) {
	size := modules + 2*quiet
	if ascii {
		return size * 2, size
	}
	return size, (size + 1) / 2
}

// renderQRText renders the module grid for a terminal.
//
// Unicode mode uses the upper half block so that one cell carries two module
// rows and each module ends up visually square. ASCII mode uses two characters
// per module for the same reason, for terminals without half-block support.
//
// Modules are forced to black on white rather than following the terminal
// theme, because a scanner needs dark-on-light contrast.
func renderQRText(grid [][]bool, quiet int, ascii bool) string {
	n := len(grid)
	size := n + 2*quiet
	at := func(row, col int) bool {
		y, x := row-quiet, col-quiet
		if y < 0 || y >= n || x < 0 || x >= n {
			return false
		}
		return grid[y][x]
	}

	var b strings.Builder
	if ascii {
		for row := 0; row < size; row++ {
			for col := 0; col < size; col++ {
				if at(row, col) {
					b.WriteString("██")
				} else {
					b.WriteString("  ")
				}
			}
			b.WriteByte('\n')
		}
		return b.String()
	}

	black := lipgloss.Color("#000000")
	white := lipgloss.Color("#FFFFFF")
	cell := lipgloss.NewStyle()
	for row := 0; row < size; row += 2 {
		for col := 0; col < size; col++ {
			top, bottom := white, white
			if at(row, col) {
				top = black
			}
			if at(row+1, col) {
				bottom = black
			}
			// "▀" paints its foreground on the top half and its background on the
			// bottom half, so one cell carries two module rows.
			b.WriteString(cell.Foreground(top).Background(bottom).Render("▀"))
		}
		b.WriteByte('\n')
	}
	return b.String()
}
