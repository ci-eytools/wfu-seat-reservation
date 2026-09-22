package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// listView is a vertical cursor with a scroll window.
//
// It owns the cursor and the window only; the caller renders each row, so the same
// widget serves rooms, periods, seats, log records and settings fields without
// knowing anything about them. Navigation is vertical on purpose: panel movement
// owns h/l, so a list must never need horizontal cursor keys.
type listView struct {
	cursor int
	top    int
	count  int
	height int
}

// SetCount replaces the item count, keeping the cursor in range.
func (l *listView) SetCount(n int) {
	l.count = max(n, 0)
	l.clamp()
}

// SetHeight sets how many rows are visible.
func (l *listView) SetHeight(h int) {
	l.height = max(h, 1)
	l.clamp()
}

func (l *listView) Move(delta int) {
	if l.count == 0 {
		return
	}
	l.cursor += delta
	l.clamp()
}

func (l *listView) GotoTop()    { l.cursor = 0; l.clamp() }
func (l *listView) GotoBottom() { l.cursor = l.count - 1; l.clamp() }

// Page moves by whole windows, for PgUp / PgDn and ctrl+u / ctrl+d.
func (l *listView) Page(pages int) {
	if l.count == 0 {
		return
	}
	l.cursor += pages * max(l.height-1, 1)
	l.clamp()
}

// Selected returns the cursor position, or false when the list is empty.
func (l *listView) Selected() (int, bool) {
	if l.count == 0 || l.cursor < 0 || l.cursor >= l.count {
		return 0, false
	}
	return l.cursor, true
}

// Window returns the half-open range of rows to render.
func (l *listView) Window() (first, last int) {
	if l.count == 0 {
		return 0, 0
	}
	first = l.top
	return first, min(first+min(l.height, l.count), l.count)
}

// Scrolled reports the 1-based cursor position and the total, for a position
// indicator.
func (l *listView) Scrolled() (position, total int) { return l.cursor + 1, l.count }

func (l *listView) clamp() {
	if l.count == 0 {
		l.cursor, l.top = 0, 0
		return
	}
	l.cursor = min(max(l.cursor, 0), l.count-1)
	visible := min(l.height, l.count)
	if l.cursor < l.top {
		l.top = l.cursor
	}
	if l.cursor >= l.top+visible {
		l.top = l.cursor - visible + 1
	}
	l.top = min(max(l.top, 0), max(l.count-visible, 0))
}

// --- row rendering ---------------------------------------------------------

// listCell is one column of a list row. The text is already fitted to the column
// width, and the style applies to that cell's own content.
type listCell struct {
	text  string
	style lipgloss.Style
}

func (m *Model) plainCell(text string, width int) listCell {
	return listCell{text: m.column(text, width), style: m.theme.RowNormal}
}

func (m *Model) mutedCell(text string, width int) listCell {
	return listCell{text: m.column(text, width), style: m.theme.RowMuted}
}

// rowStyle returns the style for a row. A selection in an unfocused panel stays
// visible but loses the accent, so the user can see where each panel was left
// without competing with the focused panel.
// rowStyle colours a list row. Only the panel that owns the keyboard paints a
// selection bar: a dimmed bar in an inactive panel is still a highlight, and two
// of them make it hard to see where the keyboard actually is.
func (m *Model) rowStyle(selected, focused bool) lipgloss.Style {
	if selected && focused {
		return m.theme.RowSelected
	}
	return m.theme.RowNormal
}

// cursorStyle styles the gutter marker. In an inactive panel the marker is kept
// but drawn faintly, so the row is still findable without competing with the
// active panel for attention.
func (m *Model) cursorStyle(focused bool) lipgloss.Style {
	if focused {
		return m.theme.RowCursor
	}
	return m.theme.FaintText
}

// rowCursor is the gutter marker. It is always present -- a space when unselected
// -- so columns never shift, and it carries the selection on a terminal with no
// colour support.
func (m *Model) rowCursor(selected bool) string {
	if selected {
		return m.glyphs.Selected
	}
	return " "
}

// listRow renders one row: the gutter cursor followed by the cells, each wrapped
// in the row's background so the selection highlight survives per-cell colours.
func (m *Model) listRow(width int, selected, focused bool, cells ...listCell) string {
	style := m.rowStyle(selected, focused)
	var out strings.Builder
	cursor := m.fit(m.rowCursor(selected), 1)
	if selected {
		// The marker keeps its own style so an inactive panel's cursor is faint
		// rather than a second highlighted bar.
		out.WriteString(m.cursorStyle(focused).Render(cursor))
		out.WriteString(style.Render(" "))
	} else {
		out.WriteString(style.Render(cursor))
		out.WriteString(style.Render(" "))
	}
	for _, cell := range cells {
		out.WriteString(style.Render(cell.style.Render(cell.text)))
	}
	return out.String()
}

// column fits text to an exact width, so header rows and data rows always align.
func (m *Model) column(text string, width int) string {
	return m.fit(text, width)
}

// padList makes a rendered list exactly the height its panel reserved, so the
// frame's bottom border never floats.
func padList(lines []string, height int) []string {
	if len(lines) > height {
		return lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

// listPosition renders "3/12" style position information.
func (m *Model) listPosition(position, total int) string {
	return fmt.Sprintf("%d/%d", position, total)
}

// committedMark is the dot that marks the item a panel has committed to, as
// opposed to the one the cursor is merely sitting on. The two are deliberately
// different things: j/k browses, space chooses, and the dot is what says which
// choice is in force.
func (m *Model) committedMark(chosen bool) listCell {
	if chosen {
		return listCell{text: m.glyphs.Dot + " ", style: m.theme.Accent}
	}
	return m.plainCell("", 2)
}
