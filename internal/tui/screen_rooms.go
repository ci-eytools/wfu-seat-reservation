package tui

import (
	"fmt"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"wfuseat/internal/chaoxing"
)

func (m *Model) roomsTrailing() string {
	day := m.rooms.day
	if day == "" {
		day = "服务器当天"
	}
	if len(m.rooms.all) == 0 {
		return day
	}
	if m.rooms.filter != "" {
		return fmt.Sprintf("%d/%d", len(m.rooms.visibleRooms()), len(m.rooms.all))
	}
	return fmt.Sprintf("%d 间", len(m.rooms.all))
}

// roomsStatusLine says what the cursor is on. The room's own facts live in the
// details card, so this line only has to answer "what will enter open?".
func (m *Model) roomsStatusLine() string {
	if m.filter.active {
		return "/ " + m.filter.input.Value()
	}
	if m.rooms.filter != "" {
		return "过滤：" + m.rooms.filter + " · esc 清除"
	}
	room, ok := m.selectedRoom()
	if !ok {
		return "未选中房间"
	}
	// A failure is reported on the panel the cursor is on, not only in the content
	// beside it: this is the line that stays visible when the panel is short.
	if room.ID == m.room.attempt && m.room.err != nil {
		return "打开失败 · " + m.room.err.Short
	}
	if !room.Selectable {
		return m.badge("服务端不可预约", sevNone)
	}
	if room.ID == m.room.id {
		return m.badge("已选中（●）", sevOK)
	}
	return m.badge("可在线预约", sevOK)
}

func (m *Model) roomsBodyLines(width, height int, focused bool) []string {
	switch {
	case m.rooms.loading:
		return toLines(m.loadingView("正在载入房间列表…", width, height), height)
	case m.rooms.err != nil:
		return toLines(m.errorView(m.rooms.err, width, height), height)
	case len(m.rooms.all) == 0:
		return toLines(m.emptyView("还没有房间数据",
			"登录后在「登录」面板按 enter，或在此按 r 载入。", width, height), height)
	}

	rooms := m.rooms.visibleRooms()
	if len(rooms) == 0 {
		return toLines(m.emptyView("没有匹配的房间",
			"按 / 修改过滤条件，或按 esc 清除。", width, height), height)
	}

	// Columns: the committed dot, the id, the name, and a glyph for whether the
	// room can be booked. The dot marks the room that is open; the trailing glyph
	// is the service's own "can this be reserved" flag, which is a different
	// question and so a different mark.
	const (
		markW   = 2
		idWidth = 5
		stateW  = 3
	)
	nameWidth := max(width-2-markW-idWidth-stateW, 6)

	first, last := m.rooms.list.Window()
	lines := make([]string, 0, height)
	for i := first; i < last; i++ {
		room := rooms[i]
		selected := i == m.rooms.list.cursor
		name := room.Name
		if name == "" {
			name = "(未命名)"
		}
		state, stateStyle := m.glyphs.Cross, m.theme.BadgeMuted
		if room.Selectable {
			state, stateStyle = m.glyphs.Ring, m.theme.BadgeOK
		}
		lines = append(lines, m.listRow(width, selected, focused,
			m.committedMark(room.ID == m.room.id),
			m.mutedCell(itoa(room.ID), idWidth),
			m.plainCell(name, nameWidth),
			listCell{text: m.column(state, stateW), style: stateStyle},
		))
	}
	return padList(lines, height)
}

func (m *Model) keyRooms(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		m.rooms.list.Move(-1)
	case key.Matches(msg, m.keys.Down):
		m.rooms.list.Move(1)
	case key.Matches(msg, m.keys.Top):
		m.rooms.list.GotoTop()
	case key.Matches(msg, m.keys.End):
		m.rooms.list.GotoBottom()
	case key.Matches(msg, m.keys.PageUp):
		m.rooms.list.Page(-1)
	case key.Matches(msg, m.keys.PageDown):
		m.rooms.list.Page(1)
	case key.Matches(msg, m.keys.Filter):
		return m.startFilter()
	case key.Matches(msg, m.keys.Space):
		return m.selectRoom()
	case key.Matches(msg, m.keys.Enter):
		return m.previewSide()
	}
	return m, nil
}

func (m *Model) selectedRoom() (chaoxing.Room, bool) {
	rooms := m.rooms.visibleRooms()
	index, ok := m.rooms.list.Selected()
	if !ok || index >= len(rooms) {
		return chaoxing.Room{}, false
	}
	return rooms[index], true
}

// syncRoomsList re-clamps the cursor after the room list or the filter changed.
func (m *Model) syncRoomsList() {
	m.rooms.list.SetCount(len(m.rooms.visibleRooms()))
}

// preselectDefaultRoom moves the cursor to the configured room, so the setting is
// actually applied rather than merely stored.
func (m *Model) preselectDefaultRoom() {
	if m.cfg.DefaultRoomID == 0 || m.room.id != 0 {
		return
	}
	for index, room := range m.rooms.visibleRooms() {
		if room.ID == m.cfg.DefaultRoomID {
			m.rooms.list.cursor = index
			m.rooms.list.clamp()
			return
		}
	}
}
