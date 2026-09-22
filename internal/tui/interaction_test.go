package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// focusPanel makes a left panel active, the way h/l do.
func focusPanel(m *Model, p Pane) {
	m.focusSide(p)
	m.layout()
}

// focusZone moves the keyboard into the right column.
func focusZone(m *Model, zone Zone) {
	m.focusZone(zone)
	m.layout()
}

// previewKey is the digit that focuses the preview card, which sits outside the
// panel group and so follows its numbers.
func previewKey() rune { return rune('0' + int(PaneDetails)) }

// describeFocus names where the keyboard is, which is what every navigation
// assertion is really about.
func describeFocus(m *Model) string {
	if m.overlay.frameOverlay() {
		if m.overlay == OverlayLogs && m.overlayFocus == 1 {
			return "records-detail"
		}
		return overlayName(m.overlay)
	}
	switch m.zone {
	case ZoneMain:
		return "main"
	case ZonePreview:
		return "preview"
	}
	if m.side == PaneSeats && m.session.phase != phaseConfirmed {
		return "login"
	}
	return m.side.title()
}

// Before a session exists the keyboard starts on the project panel, because the
// QR login is what it is for; logging in moves it to the room list.
func TestFocusStartsOnTheProjectPanel(t *testing.T) {
	m := newTestModel(t)
	resize(t, m, 120, 40)
	if m.zone != ZoneSide || m.side != PaneStatus {
		t.Fatalf("focus = %v, want the project panel", describeFocus(m))
	}
	if m.side.title() != "账号" {
		t.Fatalf("active panel = %q, want the project panel", m.side.title())
	}
	if m.overlayFocus != 0 {
		t.Fatalf("overlay focus = %d, want 0", m.overlayFocus)
	}

	// Focus survives a resize, whichever panels the new size allows.
	focusZone(m, ZonePreview)
	resize(t, m, 60, 24)
	if m.zone != ZonePreview {
		t.Fatalf("resize moved focus to %v", describeFocus(m))
	}
	resize(t, m, 200, 60)
	if m.zone != ZonePreview {
		t.Fatalf("growing the terminal moved focus to %v", describeFocus(m))
	}
}

// h/l switch the left panels, and every panel keeps its size while the active one
// changes: the column never reflows, which is what makes it a panel group.
func TestHorizontalKeysSwitchLeftPanels(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	resize(t, m, 120, 40)
	focusPanel(m, PaneRooms)
	m.rooms.list.cursor = 1

	sizes := func() map[Pane]rect {
		out := map[Pane]rect{}
		for _, slot := range m.panels {
			if slot.owner.zone == ZoneSide {
				out[slot.owner.side] = slot.rect
			}
		}
		return out
	}
	before := sizes()

	for i, want := range []string{"时段", "座位", "预订记录", "账号"} {
		feed(t, m, keyMsg('l'))
		if got := describeFocus(m); got != want {
			t.Fatalf("l step %d focused %q, want %q", i+1, got, want)
		}
	}
	last := sidePanes[len(sidePanes)-1].title()
	feed(t, m, keyMsg('h'))
	if got := describeFocus(m); got != last {
		t.Fatalf("h focused %q, want the panel before the first (%q)", got, last)
	}
	// h from the first panel wraps to the last.
	focusPanel(m, PaneStatus)
	feed(t, m, keyMsg('h'))
	if got := describeFocus(m); got != last {
		t.Fatalf("h at the first panel focused %q, want the last panel %q", got, last)
	}

	after := sizes()
	for side, size := range before {
		if after[side] != size {
			t.Errorf("panel %v changed size with focus: %v -> %v", side.title(), size, after[side])
		}
	}
	if m.rooms.list.cursor != 1 {
		t.Fatalf("switching panels moved a list cursor to %d", m.rooms.list.cursor)
	}
}

// enter goes from the panel group into the content, and esc comes back: the right
// column belongs to the left panel, and esc is the way out of it.
func TestEnterEntersContentAndEscapeReturnsToThePanelGroup(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	resize(t, m, 120, 40)
	focusPanel(m, PaneRooms)

	feed(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.zone != ZoneMain {
		t.Fatalf("enter left the keyboard at %v, want the content", describeFocus(m))
	}
	feed(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.zone != ZoneSide || m.side != PaneRooms {
		t.Fatalf("esc landed on %v, want the room panel", describeFocus(m))
	}

	// A key that means nothing in the group must not change the level either.
	focusZone(m, ZonePreview)
	feed(t, m, keyMsg(' '))
	if m.zone != ZonePreview {
		t.Fatalf("an unused key changed the level to %v", describeFocus(m))
	}
	feed(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.zone != ZoneSide {
		t.Fatalf("esc from the preview gave %v", describeFocus(m))
	}

	// The seat panel's content is the seat's own report, which is where enter
	// lands once the room is open.
	focusPanel(m, PaneSeats)
	feed(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.zone != ZoneMain {
		t.Fatalf("enter on the seat panel gave %v, want the seat's report", describeFocus(m))
	}
	if _, ok := m.selectedSeatNumber(); !ok {
		t.Fatal("the fixture seat list should have a selection")
	}
}

// tab walks the levels in order and wraps, so every region stays reachable.
func TestTabWalksTheLevels(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	resize(t, m, 120, 40)
	focusPanel(m, PaneSeats)

	ring := []string{"main", "preview", "座位"}
	for i, want := range ring {
		feed(t, m, tea.KeyMsg{Type: tea.KeyTab})
		if got := describeFocus(m); got != want {
			t.Fatalf("tab %d focused %q, want %q", i+1, got, want)
		}
	}
	feed(t, m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if got := describeFocus(m); got != "preview" {
		t.Fatalf("shift+tab focused %q, want the preview card", got)
	}
}

// The digits printed in the panel titles focus those panels directly, and the
// preview card has a number of its own.
func TestPanelDigitsFocusPanels(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	resize(t, m, 120, 40)

	for i, side := range sidePanes {
		feed(t, m, keyMsg(rune('0'+i)))
		if m.side != side || m.zone != ZoneSide {
			t.Fatalf("%d focused %v, want the %v panel", i, describeFocus(m), side.title())
		}
	}
	// The preview card sits outside the panel group, so its number follows them.
	feed(t, m, keyMsg(rune('0'+len(sidePanes))))
	if m.zone != ZonePreview {
		t.Fatalf("the preview card's number focused %v, want the preview", describeFocus(m))
	}
	for i, side := range sidePanes {
		want := "[" + itoa(i) + "]"
		if title := paneTitle(side); !strings.Contains(title, want) {
			t.Errorf("panel title %q does not advertise %q", title, want)
		}
	}
	want := "[" + itoa(int(PaneDetails)) + "]"
	if title := paneTitle(PaneDetails); !strings.Contains(title, want) {
		t.Errorf("the preview card's title %q does not advertise %s", title, want)
	}
}

// j/k move inside the focused panel and must never change panels.
func TestVerticalMovementNeverChangesPanel(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	resize(t, m, 120, 40)
	focusPanel(m, PaneRooms)

	start := describeFocus(m)
	feed(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := describeFocus(m); got != start {
		t.Fatalf("j changed panels from %q to %q", start, got)
	}
	m.rooms.list.GotoTop()
	feed(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.rooms.list.cursor != 1 {
		t.Fatalf("j did not move the cursor (cursor %d)", m.rooms.list.cursor)
	}
	feed(t, m, keyMsg('k'))
	if m.rooms.list.cursor != 0 {
		t.Fatalf("k did not move the cursor back (cursor %d)", m.rooms.list.cursor)
	}
	if got := describeFocus(m); got != start {
		t.Fatalf("k changed panels from %q to %q", start, got)
	}
}

// Exactly one panel owns the keyboard at any time, in every view.
func TestExactlyOneRegionOwnsTheKeyboard(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	resize(t, m, 120, 40)

	for _, side := range sidePanes {
		for _, zone := range []Zone{ZoneSide, ZoneMain, ZonePreview} {
			m.focusSide(side)
			m.zone = zone
			owners := 0
			for _, slot := range m.panels {
				if m.slotFocused(slot) {
					owners++
				}
			}
			if owners != 1 {
				t.Fatalf("%v/%v has %d panel owners", side.title(), zone, owners)
			}
		}
	}
	for _, slot := range m.panels {
		if slot.owner.zone == ZoneNone && m.slotFocused(slot) {
			t.Fatalf("the informational panel %q must never own the keyboard", slot.title)
		}
	}

	// The utility views have their own panels.
	for _, overlay := range []Overlay{OverlayLogs, OverlayForm} {
		m.openOverlay(overlay)
		for index := 0; index < m.viewPaneCount(); index++ {
			m.overlayFocus = index
			owners := 0
			for _, slot := range m.panels {
				if m.slotFocused(slot) {
					owners++
				}
			}
			if owners != 1 {
				t.Fatalf("%s: focus %d has %d panel owners", overlayName(overlay), index, owners)
			}
		}
		m.closeOverlay()
	}

	// A resize preserves the level rather than resetting it.
	m.zone = ZoneMain
	resize(t, m, 60, 24)
	if m.zone != ZoneMain {
		t.Fatalf("resize left focus at %v", describeFocus(m))
	}
}

func TestEscapeCancelsThenStepsFocusBack(t *testing.T) {
	esc := tea.KeyMsg{Type: tea.KeyEsc}

	t.Run("closes the help modal", func(t *testing.T) {
		m := newTestModel(t)
		resize(t, m, 120, 40)
		feed(t, m, keyMsg('?'))
		if m.overlay != OverlayHelp {
			t.Fatal("help did not open")
		}
		feed(t, m, esc)
		if m.overlay != OverlayNone {
			t.Fatal("esc did not close the help modal")
		}
	})

	t.Run("cancels an active filter", func(t *testing.T) {
		m := newTestModel(t)
		populate(m)
		resize(t, m, 120, 40)
		focusPanel(m, PaneRooms)
		feed(t, m, keyMsg('/'))
		if !m.filter.active {
			t.Fatal("filter did not open")
		}
		feed(t, m, keyMsg('1'))
		feed(t, m, esc)
		if m.filter.active {
			t.Fatal("esc did not close the filter")
		}
		if m.rooms.filter != "" {
			t.Fatalf("esc left the filter as %q, want it cleared", m.rooms.filter)
		}
	})

	t.Run("reverts unsaved settings", func(t *testing.T) {
		m := newTestModel(t)
		resize(t, m, 120, 40)
		m.focusSide(PaneStatus)
		m.session.phase = phaseConfirmed
		m.zone = ZoneMain
		original := m.cfg.Proxy
		m.settings.draft.Proxy = "http://127.0.0.1:1"
		m.settings.dirty = true
		feed(t, m, esc)
		if m.settings.dirty {
			t.Fatal("esc left the settings dirty")
		}
		if m.settings.draft.Proxy != original {
			t.Fatalf("esc did not revert the draft: %q", m.settings.draft.Proxy)
		}
	})

	t.Run("cancels a pending scan", func(t *testing.T) {
		m := newTestModel(t)
		resize(t, m, 120, 40)
		m.room.scanning = true
		feed(t, m, esc)
		if m.room.scanning {
			t.Fatal("esc did not stop the scan")
		}
	})

	t.Run("cancels a pending scan before the QR", func(t *testing.T) {
		m := newTestModel(t)
		resize(t, m, 130, 45)
		m.session.phase = phaseWaiting
		m.session.qrGrid = make([][]bool, 21)
		for i := range m.session.qrGrid {
			m.session.qrGrid[i] = make([]bool, 21)
		}
		feed(t, m, esc)
		if len(m.session.qrGrid) != 0 {
			t.Fatal("esc did not cancel the pending scan")
		}
	})

	t.Run("closes a utility view", func(t *testing.T) {
		for _, overlay := range []Overlay{OverlayLogs, OverlayForm} {
			m := newTestModel(t)
			populate(m)
			resize(t, m, 120, 40)
			focusPanel(m, PaneSeats)
			m.openOverlay(overlay)
			feed(t, m, esc)
			if m.overlay != OverlayNone {
				t.Fatalf("esc did not close the %s view", overlayName(overlay))
			}
			if m.side != PaneSeats || m.zone != ZoneSide {
				t.Fatalf("closing the %s view landed on %v, want the panel it was opened from",
					overlayName(overlay), describeFocus(m))
			}
		}
	})

	t.Run("steps out of the content", func(t *testing.T) {
		m := newTestModel(t)
		populate(m)
		resize(t, m, 120, 40)

		// Anywhere in the right column, esc returns to the panel group without
		// changing which panel is active.
		for _, zone := range []Zone{ZoneMain, ZonePreview} {
			focusPanel(m, PanePeriods)
			m.zone = zone
			feed(t, m, esc)
			if m.zone != ZoneSide {
				t.Fatalf("esc from %v gave %v", zone, describeFocus(m))
			}
			if m.side != PanePeriods {
				t.Fatalf("esc changed the active panel to %v", m.side.title())
			}
		}

		// Already in the group, esc has nothing to cancel.
		feed(t, m, esc)
		if m.zone != ZoneSide || m.side != PanePeriods {
			t.Fatalf("esc in the group moved to %v", describeFocus(m))
		}
	})
}

// q is a text character whenever a field has focus, never a quit.
func TestQDoesNotQuitWhileTyping(t *testing.T) {
	t.Run("filter", func(t *testing.T) {
		m := newTestModel(t)
		populate(m)
		resize(t, m, 120, 40)
		focusPanel(m, PaneRooms)
		feed(t, m, keyMsg('/'))
		feed(t, m, keyMsg('q'))
		if !strings.Contains(m.filter.input.Value(), "q") {
			t.Fatalf("q was not typed into the filter (value %q)", m.filter.input.Value())
		}
		if m.overlay == OverlayConfirm {
			t.Fatal("q opened the quit confirmation while filtering")
		}
	})

	t.Run("settings editor", func(t *testing.T) {
		m := newTestModel(t)
		resize(t, m, 120, 40)
		m.focusSide(PaneStatus)
		m.session.phase = phaseConfirmed
		m.zone = ZoneMain
		m.settings.list.cursor = 0
		feed(t, m, tea.KeyMsg{Type: tea.KeyEnter})
		if !m.settings.editing {
			t.Fatal("enter did not start editing")
		}
		feed(t, m, keyMsg('q'))
		if !strings.HasSuffix(m.settings.input.Value(), "q") {
			t.Fatalf("q was not typed into the field (value %q)", m.settings.input.Value())
		}
	})
}

// Ctrl+C must always quit, whatever has focus.
func TestCtrlCAlwaysQuits(t *testing.T) {
	states := []struct {
		name  string
		setup func(m *Model)
	}{
		{"idle", func(m *Model) {}},
		{"filtering", func(m *Model) { focusPanel(m, PaneRooms); feed(t, m, keyMsg('/')) }},
		{"editing settings", func(m *Model) {
			m.focusSide(PaneStatus)
			m.session.phase = phaseConfirmed
			m.zone = ZoneMain
			m.settings.editing = true
		}},
		{"in a utility view", func(m *Model) { m.openOverlay(OverlayLogs) }},
		{"help modal", func(m *Model) { m.overlay = OverlayHelp }},
		{"confirm modal", func(m *Model) { m.overlay = OverlayConfirm }},
	}
	for _, tc := range states {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			populate(m)
			resize(t, m, 120, 40)
			tc.setup(m)

			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			if cmd == nil {
				t.Fatal("ctrl+c produced no command")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Fatal("ctrl+c did not quit")
			}
		})
	}
}

// Space toggles the focused checkbox and nothing else.
func TestSpaceTogglesCheckboxSetting(t *testing.T) {
	m := newTestModel(t)
	resize(t, m, 120, 40)
	m.focusSide(PaneStatus)
	m.session.phase = phaseConfirmed
	m.zone = ZoneMain

	toggleIndex := -1
	for i, field := range settingFields {
		if field.Kind == settingToggle {
			toggleIndex = i
			break
		}
	}
	if toggleIndex < 0 {
		t.Fatal("expected at least one checkbox setting")
	}

	// Space on a text field is a no-op rather than an accidental edit.
	m.settings.list.cursor = 0
	before := m.settings.draft
	feed(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if m.settings.draft != before {
		t.Fatal("space changed a text field")
	}

	m.settings.list.cursor = toggleIndex
	if !m.settingsCursorIsToggle() {
		t.Fatal("expected the cursor to be on the checkbox")
	}
	wasOn := m.settings.draft.AllowSubmit
	feed(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if m.settings.draft.AllowSubmit == wasOn {
		t.Fatal("space did not toggle the checkbox")
	}
	if !m.settings.dirty {
		t.Fatal("toggling should mark the settings dirty")
	}
}

// Enter always performs the primary action of the current level, and it is what
// carries the keyboard from a panel into its content.
func TestEnterIsContextual(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	prepared := m.selection
	resize(t, m, 120, 40)

	// The room panel: space commits the room, enter only shows its preview.
	focusPanel(m, PaneRooms)
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatal("enter on the room panel must not act; space does that")
	}
	if m.zone != ZoneMain {
		t.Fatalf("enter on the room panel gave %v, want its preview", describeFocus(m))
	}
	focusPanel(m, PaneRooms)
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace}); cmd == nil {
		t.Fatal("space on a selectable room produced no action")
	}
	// Selecting is selecting: the load starts, and the keyboard stays on the panel
	// that asked -- nothing jumps to the next item.
	if m.side != PaneRooms {
		t.Fatalf("space left the room panel (%v); selecting must not move on", m.side.title())
	}

	// The content: enter moves the chain on without acting, space acts. This
	// fixture has no seat page, so the assertions are about routing, and the
	// replies are left alone on purpose.
	focusPanel(m, PaneRooms)
	focusZone(m, ZoneMain)
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatal("enter in the room content must not act; it moves the chain on")
	}
	if m.side != PanePeriods {
		t.Fatalf("enter in the room content left the chain at %v, want the day panel",
			m.side.title())
	}
	focusPanel(m, PaneRooms)
	focusZone(m, ZoneMain)
	// The previous step is still in flight; a second request would be coalesced
	// into it, so settle it first to test the routing rather than the guard.
	m.room.loadingRoom = false
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace}); cmd == nil {
		t.Fatal("space in the room content produced no action")
	}

	// The seat panel: enter previews the map, space commits the seat.
	m.selection = nil
	focusPanel(m, PaneSeats)
	feed(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.zone != ZoneMain {
		t.Fatalf("enter on the seat panel gave %v, want the seat map", describeFocus(m))
	}
	if m.selection != nil {
		t.Fatal("enter on the seat panel must not commit a seat; space does that")
	}
	// This fixture never opened a seat page, so no seat can be signed: space on a
	// seat is a pre-order instead, which is remembered in the project.
	focusPanel(m, PaneSeats)
	before := len(m.preorders.items)
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace}); cmd == nil {
		t.Fatal("space on the seat panel produced no work")
	}
	if len(m.preorders.items) != before+1 {
		t.Fatalf("space did not record a pre-order (%d -> %d)", before, len(m.preorders.items))
	}
	// The keyboard stays on the seat: a pre-order is local memory, and the record is
	// reachable with its own panel without losing the place in the seat list.
	if m.side != PaneSeats {
		t.Fatalf("a pre-order moved the keyboard to %v, want the seat panel", m.side.title())
	}
	feed(t, m, keyMsg('4'))
	if m.side != PaneRecords {
		t.Fatalf("4 did not open %s, got %v", paneTitle(PaneRecords), m.side.title())
	}
	if rows := plainLines(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneSide && slot.owner.side == PaneRecords
	})); !strings.Contains(rows, m.preorders.items[0].SeatNum) {
		t.Errorf("the pre-order panel does not list the new record:\n%s", rows)
	}
	// Put the keyboard back where the rest of the test expects it.
	focusPanel(m, PaneSeats)

	// The preview card's own keys: enter goes deeper into the encoding, and space
	// asks to submit, which prints the request first.
	m.selection = prepared
	focusZone(m, ZonePreview)
	feed(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.overlay != OverlayForm {
		t.Fatalf("enter on the preview card gave %v, want the encoding", overlayName(m.overlay))
	}
	// space asks to submit; this fixture never opened a seat page, so there is no
	// target to submit to and the refusal is recorded rather than guessed at.
	m.closeOverlay()
	m.write.err = nil
	feed(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if m.overlay == OverlayConfirm {
		t.Fatal("space submitted without a known target")
	}
	if m.write.err == nil {
		t.Fatal("the refusal to submit without a target was not recorded")
	}

	// With nothing prepared and no seat chosen, enter prepares what it can instead
	// of opening an empty view.
	m.selection = nil
	m.room = roomState{}
	focusZone(m, ZonePreview)
	feed(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.overlay != OverlayNone {
		t.Fatalf("enter with no preview opened %v", overlayName(m.overlay))
	}
}

// The help bar must name the keys that matter for the focused panel, and stay
// short enough to read.
func TestHelpBarIsContextual(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	resize(t, m, 120, 40)

	bar := func() string {
		lines := strings.Split(m.View(), "\n")
		return lines[len(lines)-1]
	}

	// Period panel: choosing a period and scanning are the important actions.
	focusPanel(m, PanePeriods)
	if line := bar(); !strings.Contains(line, "移动") || !strings.Contains(line, "扫描") {
		t.Errorf("period help bar should mention movement and scanning: %q", line)
	}

	// Seat panel: selecting is space and opening the map is enter, so the bar has to
	// name both -- they stopped being the same key.
	focusPanel(m, PaneSeats)
	if line := bar(); !strings.Contains(line, "space") || !strings.Contains(line, "选中") ||
		!strings.Contains(line, "enter") {
		t.Errorf("seat help bar should mention selecting and entering: %q", line)
	}

	// Every view names its escape hatches and fits the terminal.
	for _, view := range everyView() {
		m := newTestModel(t)
		populate(m)
		resize(t, m, 120, 40)
		view.apply(m)
		m.layout()
		lines := strings.Split(m.View(), "\n")
		line := lines[len(lines)-1]
		for _, want := range []string{"帮助", "退出"} {
			if !strings.Contains(line, want) {
				t.Errorf("%s help bar is missing %q: %q", view.name, want, line)
			}
		}
		if lipgloss.Width(line) > m.width {
			t.Errorf("%s help bar is %d cells wide in a %d-cell terminal", view.name, lipgloss.Width(line), m.width)
		}
	}
}

// The help modal groups shortcuts by what they do.
func TestHelpModalIsGrouped(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	resize(t, m, 120, 62)
	m.layout()
	m.overlay = OverlayHelp

	view := m.View()
	for _, want := range []string{"导航", "面板", "滚动", "操作", "视图", "程序", "上移", "上一面板"} {
		if !strings.Contains(view, want) {
			t.Errorf("help modal is missing %q:\n%s", want, view)
		}
	}
	// Vertical and horizontal movement must be documented as different things.
	for _, want := range []string{"↓/j", "←/h", "→/l"} {
		if !strings.Contains(view, want) {
			t.Errorf("help modal does not document %q", want)
		}
	}
}

// The seat list is the room's numbering, so it is complete as soon as the room is
// open; what each seat is doing right now is reported in the content panel.
func TestSeatListIsStaticAndTheContentReportsOccupancy(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	resize(t, m, 120, 40)
	focusPanel(m, PaneSeats)

	list := strings.Join(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneSide && slot.owner.side == PaneSeats
	}), "\n")
	for _, number := range []string{"001", "002", "003"} {
		if !strings.Contains(list, number) {
			t.Errorf("the static seat list is missing %s:\n%s", number, list)
		}
	}
	for _, word := range []string{"空闲", "已占用", "不可选", "不可用"} {
		if strings.Contains(list, word) {
			t.Errorf("the static seat list must not show dynamic state (%q):\n%s", word, list)
		}
	}

	// The content reports the real state for the seat being viewed.
	focusZone(m, ZoneMain)
	content := strings.Join(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneMain
	}), "\n")
	if !strings.Contains(content, "空闲") {
		t.Errorf("the seat map does not report the seat's state:\n%s", content)
	}
	if !strings.Contains(content, "位置") {
		t.Errorf("the seat map does not report a position:\n%s", content)
	}
	if got := strings.Count(content, "001"); got == 0 {
		t.Errorf("the seat map does not draw the seats:\n%s", content)
	}
}

// Filtering seats matches the seat number and the status word.
func TestSeatFilterMatchesStatus(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	resize(t, m, 120, 40)
	focusPanel(m, PaneSeats)

	m.room.filter = "02"
	if got := len(m.visibleSeatNumbers()); got == 0 {
		t.Fatal("filtering by number matched nothing")
	}
	m.room.filter = "999"
	if got := len(m.visibleSeatNumbers()); got != 0 {
		t.Fatalf("filtering by an absent number matched %d seats, want 0", got)
	}
}

// The ASCII fallback must keep the frame rectangular: ASCII mode is what a
// terminal without Unicode support gets, and a broken frame is still broken.
func TestASCIIFallbackKeepsFramesAligned(t *testing.T) {
	for _, size := range []struct{ w, h int }{{40, 22}, {80, 24}, {120, 40}} {
		for _, view := range everyView() {
			m := newTestModel(t)
			populate(m)
			m.cfg.ASCIIOnly = true
			m.glyphs = newGlyphs(true)
			resize(t, m, size.w, size.h)
			view.apply(m)
			m.layout()

			rendered := m.View()
			lines := strings.Split(rendered, "\n")
			want := lipgloss.Width(lines[0])
			for i, line := range lines {
				if got := lipgloss.Width(line); got != want {
					t.Errorf("ascii %s %dx%d: line %d is %d cells but line 0 is %d",
						view.name, size.w, size.h, i, got, want)
					break
				}
			}
			if !strings.Contains(rendered, "+") {
				t.Errorf("ascii %s: frame characters are missing", view.name)
			}
			if strings.Contains(rendered, "┌") || strings.Contains(rendered, "┬") {
				t.Errorf("ascii %s: unicode frame characters leaked into ASCII mode", view.name)
			}
		}
	}
}

// With colour enabled, the focused panel's whole border must actually be painted
// in the accent colour. Test output normally renders without colour (the profile
// follows the terminal), so the profile is forced here.
func TestFocusedBorderIsPaintedWithAccent(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })

	m := newTestModel(t)
	populate(m)
	resize(t, m, 100, 30)
	focusPanel(m, PaneSeats)

	probe := m.theme.PanelBorderFocus.Render("X")
	accent := probe[:strings.Index(probe, "X")]
	if accent == "" {
		t.Skip("the accent colour renders without an escape sequence")
	}

	view := m.View()
	if !strings.Contains(view, accent) {
		t.Fatal("the focused panel's border is not painted")
	}

	// Find the focused panel's rectangle and check each of its four sides, plus
	// the status divider, carries the accent.
	var target *panelSlot
	for i := range m.panels {
		if m.slotFocused(m.panels[i]) {
			target = &m.panels[i]
			break
		}
	}
	if target == nil {
		t.Fatal("no focused panel")
	}
	r := target.rect
	lines := strings.Split(view, "\n")
	// The header occupies the first line, so frame row y is view line y+headerHeight.
	row := func(y int) string {
		index := y + headerHeight
		if index < 0 || index >= len(lines) {
			return ""
		}
		return lines[index]
	}
	accented := func(y int) bool { return strings.Contains(row(y), accent) }
	if !accented(r.y) {
		t.Errorf("top border of the focused panel is not accented: %q", row(r.y))
	}
	if !accented(r.y + r.h - 1) {
		t.Errorf("bottom border of the focused panel is not accented: %q", row(r.y+r.h-1))
	}
	if !accented(r.y+1) || !accented(r.y+r.h-2) {
		t.Errorf("the focused panel's vertical edges are not accented")
	}
	if m.slotHasStatus(*target) && !accented(r.y+r.h-3) {
		t.Errorf("the focused panel's status divider is not accented: %q", row(r.y+r.h-3))
	}
}
