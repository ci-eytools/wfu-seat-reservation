package tui

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"strings"
	"testing"
	"time"
	"wfuseat/internal/chaoxing"
	"wfuseat/internal/schedule"
)

func TestAutoWeeklyIndependentIntervalsAndCopy(t *testing.T) {
	m := accountModel(t, t.TempDir(), "alice", &apiFake{})
	m.session.phase = phaseConfirmed
	m.room.id = 6299
	m.rooms.day = "2030-01-04"
	m.room.slots = []chaoxing.Slot{{StartTime: "08:00", EndTime: "09:00"}, {StartTime: "09:00", EndTime: "10:00"}, {StartTime: "14:00", EndTime: "15:00"}}
	m.focusSide(PanePeriods)
	m.setDayRow(dayRows)
	m.keyPeriods(tea.KeyMsg{Type: tea.KeySpace})
	m.keyPeriods(tea.KeyMsg{Type: tea.KeyEnter})
	m.repeatModeCursor = 2
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if m.repeatMode != 2 || m.repeatWeek[0].Enabled {
		t.Fatal("weekly mode not selected")
	}
	m.repeatFocus = 4
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m.repeatFocus = 0
	m.room.slotIndex = 0
	m.selectPeriodFromGrid()
	m.room.slotIndex = 1
	m.selectPeriodFromGrid()
	if m.repeatWeek[0].Start != "08:00" || m.repeatWeek[0].End != "10:00" {
		t.Fatal(m.repeatWeek)
	}
	m.repeatFocus = 4
	m.Update(keyMsg('j'))
	if m.repeatWeekCursor != 1 || len(m.room.endpoints) != 0 {
		t.Fatal("Monday interval leaked to Tuesday")
	}
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.room.slotIndex = 2
	m.selectPeriodFromGrid()
	if m.repeatWeek[1].Start != "14:00" || m.repeatWeek[0].Start != "08:00" {
		t.Fatal(m.repeatWeek)
	}
	m.repeatFocus = 4
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if m.repeatWeek[1].Enabled || m.repeatWeek[1].Start != "14:00" {
		t.Fatal("disable discarded interval")
	}
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m.repeatFocus = 5
	m.repeatCopy = 1
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if m.repeatWeek[0].Start != "14:00" || m.repeatWeek[2].Enabled || m.repeatWeek[2].Start != "" {
		t.Fatal("copy changed disabled dates")
	}
	m.repeatCopy = 0
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	for i, p := range m.repeatWeek {
		if i < 5 && (!p.Enabled || p.Start != "14:00") {
			t.Fatal(m.repeatWeek)
		}
		if i >= 5 && p.Enabled {
			t.Fatal("weekend enabled by workday copy")
		}
	}
	for _, width := range []int{52, 110, 180} {
		lines := m.periodDetailBody(width, 28, true)
		if len(lines) != 28 {
			t.Fatal("height overflow", len(lines))
		}
		for _, line := range lines {
			if lipgloss.Width(line) > width {
				t.Fatal("width overflow", width, line)
			}
		}
		view := strings.Join(lines, "\n")
		for _, want := range []string{"周一", "周日", "结束条件", "14:00–15:00"} {
			if !strings.Contains(view, want) {
				t.Fatal("missing", want, view)
			}
		}
	}
}

func TestWeeklyEnterAdvancesDayWithoutLeavingPeriods(t *testing.T) {
	m := accountModel(t, t.TempDir(), "alice", &apiFake{})
	m.session.phase = phaseConfirmed
	m.focusSide(PanePeriods)
	m.days.cursor = dayRows
	m.zone = ZoneMain
	m.repeatMode = 2
	m.repeatFocus = 0
	m.repeatWeekCursor = 6
	m.keyPeriodGrid(tea.KeyMsg{Type: tea.KeyEnter})
	if m.side != PanePeriods || m.repeatWeekCursor != 0 || m.repeatFocus != 0 {
		t.Fatal("Enter left the weekly editor")
	}
	if m.repeatWeek[0].Enabled {
		t.Fatal("navigation enabled Monday")
	}
}

func TestRuleHighlightBelongsOnlyToFocusedOption(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
	m := accountModel(t, t.TempDir(), "alice", &apiFake{})
	m.repeatMode = 0
	m.repeatModeCursor = 1
	m.repeatFocus = 1
	row := m.repeatHeader(110, true)[2]
	want := m.rowStyle(true, true).Render("○ 周一～周五")
	if !strings.Contains(row, want) {
		t.Fatal("focused option missing highlight", row)
	}
	if strings.Count(row, "48;2;") != 1 {
		t.Fatal("outer row or unselected options also highlighted", row)
	}
	for _, tc := range []struct {
		focus  int
		active bool
	}{{0, true}, {2, true}, {4, true}, {1, false}} {
		m.repeatFocus = tc.focus
		row = m.repeatHeader(110, tc.active)[2]
		if strings.Contains(row, "48;2;") {
			t.Fatal("rule highlight survived focus change", row)
		}
		if !strings.Contains(row, "● 每天") {
			t.Fatal("committed rule marker lost", row)
		}
	}
}

func TestWeeklyHorizontalKeysMoveFocusNotDayOrInterval(t *testing.T) {
	m := accountModel(t, t.TempDir(), "alice", &apiFake{})
	m.session.phase = phaseConfirmed
	m.focusSide(PanePeriods)
	m.days.cursor = dayRows
	m.zone = ZoneMain
	m.repeatMode = 2
	m.repeatFocus = 4
	m.repeatWeekCursor = 2
	m.room.slotIndex = 3
	for _, tc := range []struct {
		key   tea.KeyMsg
		focus int
	}{
		{keyMsg('l'), 0}, {keyMsg('l'), 0}, {keyMsg('h'), 4}, {keyMsg('h'), 4},
		{tea.KeyMsg{Type: tea.KeyRight}, 0}, {tea.KeyMsg{Type: tea.KeyLeft}, 4},
	} {
		m.Update(tc.key)
		if m.repeatFocus != tc.focus || m.repeatWeekCursor != 2 || m.room.slotIndex != 3 || m.side != PanePeriods {
			t.Fatal("horizontal key changed selection instead of focus", tc, m.repeatFocus, m.repeatWeekCursor, m.room.slotIndex)
		}
	}
	m.Update(keyMsg('j'))
	if m.repeatWeekCursor != 3 {
		t.Fatal("vertical day movement broken")
	}
	if !strings.Contains(m.periodDetailStatus(), "进入时段") {
		t.Fatal("missing focus hint")
	}
	m.Update(keyMsg('l'))
	if !strings.Contains(m.periodDetailStatus(), "返回星期") {
		t.Fatal("missing return hint")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.repeatWeekCursor != 4 || m.side != PanePeriods {
		t.Fatal("Enter no longer advances to next weekday")
	}
}

func TestWeeklyIntervalSelectionAutomaticallyTogglesOnlyCurrentDay(t *testing.T) {
	m := accountModel(t, t.TempDir(), "alice", &apiFake{})
	m.session.phase = phaseConfirmed
	m.focusSide(PanePeriods)
	m.days.cursor = dayRows
	m.zone = ZoneMain
	m.repeatMode = 2
	m.repeatFocus = 0
	m.repeatWeekCursor = 2
	m.room.slots = []chaoxing.Slot{{StartTime: "08:00", EndTime: "09:00"}, {StartTime: "09:00", EndTime: "10:00"}}
	m.room.periodExplicit = true
	m.room.slotIndex = 0
	m.selectPeriodFromGrid()
	if !m.repeatWeek[2].Enabled || m.repeatWeek[2].Start != "08:00" {
		t.Fatal("first endpoint did not enable day")
	}
	m.room.slotIndex = 1
	m.selectPeriodFromGrid()
	if !m.repeatWeek[2].Enabled || m.repeatWeek[2].End != "10:00" {
		t.Fatal("range not saved")
	}
	m.room.slotIndex = 0
	m.selectPeriodFromGrid()
	if !m.repeatWeek[2].Enabled || m.repeatWeek[2].Start != "09:00" {
		t.Fatal("remaining endpoint should keep day enabled")
	}
	m.room.slotIndex = 1
	m.selectPeriodFromGrid()
	if m.repeatWeek[2].Enabled || m.repeatWeek[2].Start != "" || m.repeatWeek[2].End != "" {
		t.Fatal("empty interval did not disable day")
	}
	for i, p := range m.repeatWeek {
		if i != 2 && (p.Enabled || p.Start != "") {
			t.Fatal("another day changed")
		}
	}
	m.selectPeriodFromGrid()
	if !m.repeatWeek[2].Enabled {
		t.Fatal("reselect did not enable day")
	}
}

func TestSelectingAutomaticLoadsIntervalsWithoutSelectingDate(t *testing.T) {
	fake := &apiFake{}
	m := accountModel(t, t.TempDir(), "alice", fake)
	if _, err := m.client.OpenHome(context.Background()); err != nil {
		t.Fatal(err)
	}
	today := time.Now().In(schedule.Zone).Format("2006-01-02")
	m.session.phase = phaseConfirmed
	m.rooms.day = today
	m.days.anchor = today
	m.room.id = 6299
	m.room.name = "reference room"
	m.focusSide(PanePeriods)
	m.setDayRow(dayRows)
	_, cmd := m.keyPeriods(tea.KeyMsg{Type: tea.KeySpace})
	if cmd == nil {
		t.Fatal("automatic selection did not load intervals")
	}
	drain(t, m, cmd, 30)
	if len(m.room.slots) == 0 || !m.repeatEnabled || m.days.cursor != dayRows {
		t.Fatal("automatic intervals missing", m.room.err, m.rooms.err)
	}
	if m.rooms.day != time.Now().In(schedule.Zone).AddDate(0, 0, 1).Format("2006-01-02") {
		t.Fatal("wrong reference date", m.rooms.day)
	}
	before := len(fake.requestedPaths())
	if m.ensureAutomaticIntervals() != nil {
		t.Fatal("cached intervals fetched again")
	}
	if len(fake.requestedPaths()) != before {
		t.Fatal("unexpected repeat request")
	}
}
