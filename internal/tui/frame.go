package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Frame geometry
//
// Every panel draws its own complete box with all four rounded corners; panels do
// not share edges. That is how lazygit's side column reads: a stack of separate
// cards rather than one region carved up by dividers.
//
//	╭─ 状态 ───────────╮
//	│ 登录    已登录   │
//	╰──────────────────╯
//	╭─ [1] 房间 ───────╮
//	│▸ 6299 101        │
//	╰──────────3 of 6──╯
//
// A column stacks panels of different heights, which is what gives the left side
// its shape: a short status card above the lists that drive the workflow.
//
// The layout only ever asks for a preferred interior height per band; zero means
// "share whatever is left", and several bands may ask for that at once.

// rect is a panel's cell rectangle inside the frame, borders included.
type rect struct{ x, y, w, h int }

// Zone is the region of the frame a panel belongs to. The frame is a hierarchy
// rather than a row of equals: the side column holds the lists, and the right
// column holds the content of whichever list is active, with the prepared
// selection below it.
type Zone int

const (
	// ZoneNone marks a panel as informational: it is drawn like every other
	// panel but never owns the keyboard.
	ZoneNone Zone = iota
	// ZoneSide is the active list in the left column.
	ZoneSide
	// ZoneMain is the right column's content for the active list.
	ZoneMain
	// ZonePreview is the right column's selection and last-write card.
	ZonePreview
	// ZoneView is a panel inside a utility view (diagnostics, settings, form).
	ZoneView
)

// panelOwner says which region a panel represents.
type panelOwner struct {
	zone Zone
	// side identifies the left list this panel shows or describes.
	side Pane
	// index is the position within a utility view.
	index int
}

// ownerNone is the owner of an informational panel.
var ownerNone = panelOwner{zone: ZoneNone}

// panelSlot is one region of the frame.
type panelSlot struct {
	owner panelOwner

	title string
	// trail sits right-aligned on the top border, count on the bottom one.
	trail  func() string
	count  func() string
	status func() string

	// rect, interiorW and interiorH are assigned by the layout pass, so the
	// widgets sized here and the box drawn later cannot disagree.
	rect                 rect
	interiorW, interiorH int

	// body renders exactly interiorH lines of interiorW cells each.
	body func(width, height int, focused bool) []string
}

// columnSpec is one vertical strip of the frame: a stack of independent panels.
type columnSpec struct {
	// width is the preferred interior width; 0 absorbs what is left.
	width int
	rows  []rowSpec
}

// rowSpec is one panel in a column stack.
type rowSpec struct {
	// height is the preferred interior height; 0 absorbs what is left.
	height int
	slot   panelSlot
}

// splitBands divides total cells into n adjacent bands, each needing fixed[i]
// cells of chrome. A zero preference absorbs what is left, and several bands may
// be flexible at once: the left column stacks two lists that must share the space
// the short status card does not use. Bands always fill the total exactly.
func splitBands(total int, want, fixed []int) []int {
	n := len(fixed)
	if n == 0 {
		return nil
	}
	chrome := 0
	for _, f := range fixed {
		chrome += f
	}
	body := max(total-chrome, n)

	sizes := make([]int, n)
	flexible := make([]int, 0, n)
	used := 0
	for i := 0; i < n; i++ {
		preferred := 0
		if i < len(want) {
			preferred = want[i]
		}
		if preferred <= 0 {
			flexible = append(flexible, i)
			continue
		}
		sizes[i] = max(preferred, 1)
		used += sizes[i]
	}
	if len(flexible) == 0 {
		// No flexible band: the last one gives back whatever it was asked for.
		flexible = append(flexible, n-1)
		used -= sizes[n-1]
		sizes[n-1] = 0
	}

	// Every flexible band is guaranteed one row before the remainder is shared,
	// so a stack of lists never collapses into nothing.
	if used+len(flexible) > body {
		// Preferences alone do not fit: shrink the largest fixed band until they
		// do, keeping one row for each of them.
		for used+len(flexible) > body {
			largest := -1
			for i := 0; i < n; i++ {
				if sizes[i] > 1 && (largest < 0 || sizes[i] > sizes[largest]) {
					largest = i
				}
			}
			if largest < 0 {
				break
			}
			sizes[largest]--
			used--
		}
	}
	for _, i := range flexible {
		sizes[i] = 1
		used++
	}
	remaining := max(body-used, 0)
	for k, i := range flexible {
		share := remaining / (len(flexible) - k)
		sizes[i] += share
		remaining -= share
	}

	out := make([]int, n)
	sum := 0
	for i := range sizes {
		out[i] = sizes[i] + fixed[i]
		sum += out[i]
	}
	// A frame smaller than the chrome can hold cannot honour the split, so the
	// longest bands give rows back: a panel is never laid out past the frame, and
	// the worst case is a panel drawn as its top border alone.
	for sum > total {
		largest := -1
		for i := range out {
			if out[i] > 1 && (largest < 0 || out[i] > out[largest]) {
				largest = i
			}
		}
		if largest < 0 {
			break
		}
		out[largest]--
		sum--
	}
	return out
}

// slotDecoration is the chrome a panel spends beyond its two border lines: the
// divider and the status line.
func (m *Model) slotDecoration(slot panelSlot) int {
	if m.slotHasStatus(slot) {
		return 2
	}
	return 0
}

// layoutFrame computes every panel rectangle and hands each panel the interior it
// will actually receive.
func (m *Model) layoutFrame(width, height int, columns []columnSpec) []panelSlot {
	colWant := make([]int, len(columns))
	colFixed := make([]int, len(columns))
	for i, column := range columns {
		colWant[i] = column.width
		colFixed[i] = 2 // one border cell on each side
	}
	colOuter := splitBands(width, colWant, colFixed)

	var slots []panelSlot
	x := 0
	for i, column := range columns {
		want := make([]int, len(column.rows))
		fixed := make([]int, len(column.rows))
		for j, row := range column.rows {
			want[j] = row.height
			fixed[j] = 2 + m.slotDecoration(row.slot)
		}
		rowOuter := splitBands(height, want, fixed)
		y := 0
		for j, row := range column.rows {
			slot := row.slot
			slot.rect = rect{x: x, y: y, w: colOuter[i], h: rowOuter[j]}
			slot.interiorW = colOuter[i] - 2
			slot.interiorH = rowOuter[j] - fixed[j]
			slots = append(slots, slot)
			y += rowOuter[j]
		}
		x += colOuter[i]
	}
	return slots
}

// slotHasStatus reports whether this panel draws a status line. The layout
// reserves room for exactly this decision, so both sides agree.
func (m *Model) slotHasStatus(slot panelSlot) bool {
	return slot.status != nil && m.slotText(slot.status) != ""
}

// slotText evaluates lazy panel metadata, which must be resolved at draw time:
// the page list's position depends on the window the layout just sized.
func (m *Model) slotText(f func() string) string {
	if f == nil {
		return ""
	}
	return f()
}

// slotFocused reports whether a panel currently owns the keyboard. Exactly one
// panel in the active view can answer yes.
func (m *Model) slotFocused(slot panelSlot) bool {
	if m.overlay.frameOverlay() {
		return slot.owner.zone == ZoneView && slot.owner.index == m.overlayFocus
	}
	switch slot.owner.zone {
	case ZoneSide:
		return m.zone == ZoneSide && slot.owner.side == m.side
	case ZoneMain:
		return m.zone == ZoneMain
	case ZonePreview:
		return m.zone == ZonePreview
	}
	return false
}

// --- rendering -------------------------------------------------------------

// renderFrame draws each panel as its own box and places the boxes side by side.
func (m *Model) renderFrame(width, height int, slots []panelSlot) string {
	if len(slots) == 0 {
		return m.constrain("", width, height)
	}
	// Too small to frame: show the focused panel's body without borders.
	if width < 8 || height < 4 {
		var lines []string
		for _, slot := range slots {
			if m.slotFocused(slot) && slot.body != nil {
				lines = slot.body(max(width, 1), max(height, 1), true)
				break
			}
		}
		if lines == nil && slots[0].body != nil {
			lines = slots[0].body(max(width, 1), max(height, 1), false)
		}
		return m.constrain(strings.Join(lines, "\n"), width, height)
	}

	type segment struct {
		x    int
		text string
	}
	rows := make([][]segment, height)
	for _, slot := range slots {
		for i, line := range m.renderPanelBox(slot) {
			y := slot.rect.y + i
			if y < 0 || y >= height {
				continue
			}
			rows[y] = append(rows[y], segment{x: slot.rect.x, text: line})
		}
	}

	var out strings.Builder
	for y := 0; y < height; y++ {
		segments := rows[y]
		sort.Slice(segments, func(a, b int) bool { return segments[a].x < segments[b].x })
		x := 0
		for _, seg := range segments {
			if seg.x > x {
				out.WriteString(strings.Repeat(" ", seg.x-x))
				x = seg.x
			}
			out.WriteString(seg.text)
			x += lipgloss.Width(seg.text)
		}
		if x < width {
			out.WriteString(strings.Repeat(" ", width-x))
		}
		if y < height-1 {
			out.WriteByte('\n')
		}
	}
	return m.constrain(out.String(), width, height)
}

// renderPanelBox draws one panel as its own complete rounded box.
func (m *Model) renderPanelBox(slot panelSlot) []string {
	r := slot.rect
	if r.w < 2 || r.h < 1 {
		return nil
	}
	focused := m.slotFocused(slot)
	frame := m.glyphs.Border
	border := m.theme.panelBorder(focused)
	inner := r.w - 2

	// Degenerate boxes keep the border rather than dropping the frame.
	if r.h < 3 {
		return []string{border.Render(frame.TopLeft + strings.Repeat(frame.Top, inner) + frame.TopRight)}
	}

	hasStatus := m.slotHasStatus(slot) && r.h >= 4
	bodyHeight := r.h - 2
	if hasStatus {
		bodyHeight -= 2
	}

	lines := []string{m.boxTop(slot, inner, focused)}
	var body []string
	if slot.body != nil {
		body = slot.body(inner, max(bodyHeight, 1), focused)
	}
	for i := 0; i < bodyHeight; i++ {
		text := ""
		if i < len(body) {
			text = body[i]
		}
		lines = append(lines, border.Render(frame.Left)+m.fit(text, inner)+border.Render(frame.Right))
	}
	if hasStatus {
		lines = append(lines,
			border.Render(frame.MiddleLeft+strings.Repeat(frame.Top, inner)+frame.MiddleRight))
		status := " " + m.clip(m.slotText(slot.status), max(inner-1, 1))
		lines = append(lines, border.Render(frame.Left)+
			m.fit(m.theme.PanelStatus.Render(status), inner)+
			border.Render(frame.Right))
	}
	lines = append(lines, m.boxBottom(slot, inner, focused))
	return lines
}

// boxTop builds "╭─ Title ───── trail ─╮", always exactly inner+2 cells wide.
func (m *Model) boxTop(slot panelSlot, inner int, focused bool) string {
	frame := m.glyphs.Border
	border := m.theme.panelBorder(focused)

	lead := ""
	left := ""
	if slot.title != "" {
		lead = frame.Top
		left = " " + slot.title + " "
	}
	right := ""
	if trailing := m.slotText(slot.trail); trailing != "" {
		right = " " + trailing + " "
	}
	free := inner - lipgloss.Width(lead) - lipgloss.Width(left) - lipgloss.Width(right)
	if free < 0 {
		right = ""
		free = inner - lipgloss.Width(lead) - lipgloss.Width(left)
	}
	if free < 0 {
		// No room for the title: keep the frame, drop the text.
		return border.Render(frame.TopLeft + strings.Repeat(frame.Top, inner) + frame.TopRight)
	}
	return border.Render(frame.TopLeft+lead) +
		m.theme.panelTitle(focused).Render(left) +
		border.Render(strings.Repeat(frame.Top, free)) +
		m.theme.MutedText.Render(right) +
		border.Render(frame.TopRight)
}

// boxBottom builds "╰──── 3 of 6 ────╯", with the count right-aligned the way
// lazygit puts a panel's position on its own bottom border.
func (m *Model) boxBottom(slot panelSlot, inner int, focused bool) string {
	frame := m.glyphs.Border
	border := m.theme.panelBorder(focused)

	label := ""
	if count := m.slotText(slot.count); count != "" {
		label = " " + count + " "
	}
	free := inner - lipgloss.Width(label)
	if free < 0 {
		return border.Render(frame.BottomLeft + strings.Repeat(frame.Top, inner) + frame.BottomRight)
	}
	return border.Render(frame.BottomLeft+strings.Repeat(frame.Top, free)) +
		m.theme.MutedText.Render(label) +
		border.Render(frame.BottomRight)
}
