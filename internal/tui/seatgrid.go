package tui

import (
	"fmt"
	"strings"

	"wfuseat/internal/chaoxing"
)

// The seat map
//
// The seat panel's list is the room's numbering, one seat per row: a stable index
// that is known as soon as the room is open. The content beside it is the same
// seats arranged as a map, which is what occupancy is actually readable in. The
// two share one cursor, so moving in either view moves the selection in both.
//
// The map uses the room's own coordinates when the service reports them, and
// otherwise flows the seats by number, saying so rather than implying a layout
// the room never described.

// seatCellWidth is a seat cell: three digits plus a state mark and the gap.
const seatCellWidth = 6

// periodCellWidth is a period cell: a bordered box plus its gap.
const periodCellWidth = 26

// seatGridBody renders the map: a detail line for the seat under the cursor, then
// as many rows of seats as fit.
func (m *Model) seatGridBody(width, height int, focused bool) []string {
	numbers := m.visibleSeatNumbers()
	if len(numbers) == 0 {
		return toLines(m.readOnlyView("只读 · 没有可显示的座位",
			"在 "+paneTitle(PaneSeats)+" 用 j/k 或过滤选择座位。", width, height), height)
	}
	lines := make([]string, 0, height)
	if height >= 3 {
		lines = append(lines, m.seatGridDetail(width))
	}
	body := max(height-len(lines), 1)
	// The service's own positions, when it gave them, arrange the map; otherwise the
	// seats flow by number, as many per row as the panel fits.
	if plan := m.seatPlan(); plan != nil {
		lines = append(lines, m.seatPlanRows(plan, width, body, focused)...)
	} else {
		lines = append(lines, m.seatGridRows(numbers, max(m.gridCols, 1), width, body, focused)...)
	}
	return padList(lines, height)
}

// seatGridDetail is the cursor's own line: what seat it is, where it is, and what
// it is doing at the period in use.
func (m *Model) seatGridDetail(width int) string {
	number, ok := m.selectedSeatNumber()
	if !ok {
		return m.theme.FaintText.Render(m.clip("○ 只读 · 未选择座位", width))
	}
	state := "未查询（space/enter 选定并查询）"
	style := m.theme.BadgeMuted
	if m.room.loadingSeats {
		state, style = "查询中…", m.theme.BadgeMuted
	} else if known, found := m.seatState(number); found {
		state, style = known.Status.String(), m.seatStatusStyle(known.Status)
	}
	where := ""
	if spot, found := m.seatSpot(number); found {
		where = seatSpotText(spot)
	}
	left := m.theme.Accent.Bold(true).Render(number) + " " + style.Render(state)
	// The commitment comes right after the state: the line is clipped to the panel,
	// and "already taken" is the one thing that must never be the part cut off.
	switch m.chosenSeats()[number] {
	case seatSelected:
		left += m.theme.Committed.Render(" · 已选中")
	case seatPreordered:
		left += m.theme.Accent.Render(" · 已预订")
	}
	if where != "" {
		left += m.theme.FaintText.Render(" · 位置 " + where)
	}
	if occupancy, cached := m.room.currentOccupancy(); cached {
		left += m.theme.FaintText.Render(fmt.Sprintf(" · 该时段 %d 空闲 / %d 占用",
			occupancy.Free, occupancy.Occupied))
	}
	return m.fit(left, width)
}

// seatGridRows renders the map itself, windowed on the cursor so the selection is
// never scrolled out of view.
func (m *Model) seatGridRows(numbers []string, columns, width, height int, focused bool) []string {
	total := len(numbers)
	rows := (total + columns - 1) / columns
	cursorRow := m.room.seats.cursor / columns
	first := 0
	if rows > height {
		first = clampInt(cursorRow-height/2, 0, rows-height)
	}
	committed := m.chosenSeats()
	lines := make([]string, 0, height)
	for row := first; row < rows && len(lines) < height; row++ {
		parts := make([]string, 0, columns)
		for col := 0; col < columns; col++ {
			index := row*columns + col
			if index >= total {
				break
			}
			number := numbers[index]
			parts = append(parts, m.seatCell(number, index == m.room.seats.cursor,
				committed[number], focused))
		}
		// Cells carry their own trailing gap, so they are concatenated rather
		// than joined with another separator.
		lines = append(lines, m.fit(strings.Join(parts, ""), width))
	}
	return lines
}

// seatCell renders one seat: its number, coloured and marked by state, with the
// cursor inverted. An unqueried period draws every cell light, because that is
// exactly what it is: no data. A seat a choice is in force for stays blue even once
// the cursor has moved off it, so "which seat did I take" is answerable at a glance;
// the signed selection is the stronger blue, because that is the one that can be
// submitted.
func (m *Model) seatCell(number string, cursor bool, commit seatCommit, focused bool) string {
	mark := m.glyphs.Bullet
	style := m.theme.BadgeMuted
	unqueried := false
	if _, cached := m.room.currentOccupancy(); !cached {
		unqueried = true
	} else if state, ok := m.seatState(number); ok {
		mark = m.seatStatusGlyph(state.Status)
		style = m.seatStatusStyle(state.Status)
	}
	text := m.fit(mark+number, seatCellWidth-1)
	for i, s := range m.candidates {
		if s == number {
			text = m.fit(fmt.Sprintf("%d:%s", i+1, number), seatCellWidth-1)
		}
	}
	if cursor {
		if !focused {
			// Inactive map: the cursor is faint, with no bar of its own.
			return m.theme.FaintText.Render(m.fit(m.glyphs.Selected+number, seatCellWidth-1)) + " "
		}
		return m.theme.RowSelected.Render(m.fit(m.glyphs.Selected+number, seatCellWidth-1)) + " "
	}
	switch commit {
	case seatSelected:
		return m.theme.Committed.Render(text) + " "
	case seatPreordered:
		return m.theme.Accent.Render(text) + " "
	}
	if unqueried {
		return m.theme.FaintText.Render(text) + " "
	}
	return style.Render(text) + " "
}

// seatGridLegend explains the marks, so the map does not need a colour key in
// somebody's head.
func (m *Model) seatGridLegend() string {
	parts := []string{
		m.seatStatusStyle(chaoxing.SeatFree).Render(m.glyphs.Dot + "空闲"),
		m.seatStatusStyle(chaoxing.SeatOccupied).Render(m.glyphs.Cross + "已占用"),
		m.theme.BadgeMuted.Render(m.glyphs.Bullet + "不可选"),
		m.theme.BadgeMuted.Render(m.glyphs.Bullet + "不可用"),
		m.theme.FaintText.Render(m.glyphs.Bullet + "未查询"),
	}
	return strings.Join(parts, " ")
}
