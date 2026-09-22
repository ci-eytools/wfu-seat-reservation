package tui

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"strings"
	"testing"
	"wfuseat/internal/chaoxing"
)

func TestPeriodListEndpointsAndViewport(t *testing.T) {
	m := accountModel(t, t.TempDir(), "张20260001", &apiFake{})
	for i := 0; i < 30; i++ {
		m.room.slots = append(m.room.slots, chaoxing.Slot{StartTime: fmt.Sprintf("%02d:%02d", 6+i/2, (i%2)*30), EndTime: fmt.Sprintf("%02d:%02d", 6+(i+1)/2, ((i+1)%2)*30)})
	}
	m.room.endpoints = []int{3, 1}
	m.room.slotIndex = 2
	text := strings.Join(m.periodGridLines(80, 8), "\n")
	for _, want := range []string{"[S]", "起点", "[E]", "终点", "> [=]", "区间内", "2/2"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing", want, text)
		}
	}
	m.room.slotIndex = 29
	for _, width := range []int{24, 80} {
		lines := m.periodGridLines(width, 8)
		text = strings.Join(lines, "\n")
		if !strings.Contains(text, "20:30–21:00") {
			t.Fatal("cursor hidden", text)
		}
		for _, line := range lines {
			if lipgloss.Width(line) > width {
				t.Fatal("overflow", width, line)
			}
		}
	}
}

func TestPeriodGroupsFollowDayPartsAndColumnNavigation(t *testing.T) {
	m := accountModel(t, t.TempDir(), "张20260001", &apiFake{})
	for i := 0; i < 30; i++ {
		m.room.slots = append(m.room.slots, chaoxing.Slot{StartTime: fmt.Sprintf("%02d:%02d", 6+i/2, i%2*30), EndTime: fmt.Sprintf("%02d:%02d", 6+(i+1)/2, (i+1)%2*30)})
	}
	m.Update(tea.WindowSizeMsg{Width: 220, Height: 50})
	m.focusSide(PanePeriods)
	m.zone = ZoneMain
	m.layout()
	m.periodWidth = 120
	groups := m.periodGroups(120)
	if len(groups) != 3 || len(groups[0].indices) != 12 || len(groups[1].indices) != 12 || len(groups[2].indices) != 6 {
		t.Fatal(groups)
	}
	text := strings.Join(m.periodGridLines(120, 16), "\n")
	for _, want := range []string{"上午", "下午", "晚间", "06:00–06:30", "20:30–21:00"} {
		if !strings.Contains(text, want) {
			t.Fatal("missing", want)
		}
	}
	m.room.slotIndex = 2
	m.movePeriodColumn(1)
	if m.room.slotIndex != 14 {
		t.Fatal("right did not retain row", m.room.slotIndex)
	}
	m.movePeriodColumn(1)
	if m.room.slotIndex != 26 {
		t.Fatal(m.room.slotIndex)
	}
	m.movePeriodColumn(-1)
	if m.room.slotIndex != 14 {
		t.Fatal(m.room.slotIndex)
	}
	if len(m.periodGroups(80)) != 2 || len(m.periodGroups(40)) != 1 {
		t.Fatal("responsive columns")
	}
}
