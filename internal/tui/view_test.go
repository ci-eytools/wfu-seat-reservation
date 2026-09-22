package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
)

func newTestModel(t *testing.T) *Model {
	t.Helper()
	cfg := config.Default()
	cfg.LogDir = t.TempDir()
	cfg.Proxy = ""
	m, err := New(cfg, "/tmp/wfuseat-test-config.json")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Close)
	return m
}

func resize(t *testing.T, m *Model, w, h int) {
	t.Helper()
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
}

// panelLines returns the rendered lines of one panel, borders included, cut to
// that panel's own columns so a test can tell its content from its neighbour's.
func panelLines(m *Model, match func(panelSlot) bool) []string {
	lines := strings.Split(m.View(), "\n")
	for _, slot := range m.panels {
		if !match(slot) {
			continue
		}
		out := make([]string, 0, slot.rect.h)
		for y := slot.rect.y; y < slot.rect.y+slot.rect.h; y++ {
			index := y + headerHeight
			if index < 0 || index >= len(lines) {
				continue
			}
			out = append(out, ansi.Cut(lines[index], slot.rect.x, slot.rect.x+slot.rect.w))
		}
		return out
	}
	return nil
}

// chaoxingSlotAt builds a slot for the fixtures.
func chaoxingSlotAt(start, end string) chaoxing.Slot {
	return chaoxing.Slot{StartTime: start, EndTime: end}
}

// testOccupancy builds the seat picture the fixture room reports for one period.
func testOccupancy() *chaoxing.Occupancy {
	occupancy := &chaoxing.Occupancy{Start: "19:30", End: "20:00"}
	for i := 1; i <= 120; i++ {
		status := chaoxing.SeatFree
		switch {
		case i == 2:
			status = chaoxing.SeatOccupied
		case i == 3:
			status = chaoxing.SeatDisabled
		case i == 4:
			status = chaoxing.SeatUnavailable
		}
		occupancy.Seats = append(occupancy.Seats, chaoxing.SeatState{
			Num: fmt.Sprintf("%03d", i), Status: status,
		})
		switch status {
		case chaoxing.SeatFree:
			occupancy.Free++
		case chaoxing.SeatOccupied:
			occupancy.Occupied++
		case chaoxing.SeatDisabled:
			occupancy.Disabled++
		case chaoxing.SeatUnavailable:
			occupancy.Unavailable++
		}
	}
	return occupancy
}

// populate fills every panel with representative data so the tests exercise the
// real content branches rather than only the empty states.
func populate(m *Model) {
	m.session.phase = phaseConfirmed
	m.session.home = &chaoxing.HomeInfo{State: "seat_home_loaded", ServerDay: "2026-09-16"}
	m.session.callback = map[string]any{"state": "web_login_confirmed"}
	m.client.Day = "2026-09-16"

	m.rooms.day = "2026-09-16"
	m.rooms.all = []chaoxing.Room{
		{ID: 6299, Name: "主校区 - 一楼 - 101自修室", Capacity: 120, Selectable: true},
		{ID: 6300, Name: "主校区 - 二楼 - 一个非常非常长的房间名称用于验证截断是否生效", Capacity: 8},
	}
	m.rooms.list.SetCount(len(m.rooms.visibleRooms()))

	m.room.id = 6299
	m.room.name = "主校区 - 一楼 - 101自修室"
	m.room.capacity = 120
	m.room.layout = chaoxing.SeatLayout{Mode: "列表模式", Start: 1, Total: 120}
	m.room.hasLayout = true
	m.room.slots = []chaoxing.Slot{
		{StartTime: "19:00", EndTime: "19:30"},
		{StartTime: "19:30", EndTime: "20:00"},
	}
	m.room.slotIndex = 1
	m.room.periods.SetCount(len(m.room.slots))
	m.room.periods.cursor = 1
	m.room.remember(testOccupancy())
	m.room.seats.SetCount(len(m.visibleSeatStates()))

	m.selection = &chaoxing.Selection{
		Day: "2026-09-16", RoomID: 6299, RoomName: "主校区 - 一楼 - 101自修室",
		SeatNum: "024", StartTime: "19:00", EndTime: "19:30",
	}

	m.logs.entries = []logEntry{
		{Method: "GET", URL: "https://office.chaoxing.com/data/apps/seat/room/list", Status: 200, ElapsedMS: 231,
			Raw: map[string]any{"status": float64(200), "method": "GET"}},
		{Method: "POST", URL: "https://office.chaoxing.com/data/apps/seat/getusedseatnums", Status: 403, ElapsedMS: 88,
			Raw: map[string]any{"status": float64(403)}},
	}
	m.logs.list.SetCount(len(m.logs.entries))
}

// testSizes are all at or above the minimum size, because below it the frame is
// replaced by the size notice and there is nothing to lay out.
var testSizes = []struct{ w, h int }{
	{38, 22},
	{60, 22},
	{78, 24},
	{80, 24},
	{100, 30},
	{120, 40},
	{200, 60},
}

// assertFits is the width invariant: every rendered line is the same width and
// none exceeds the terminal.
func assertFits(t *testing.T, m *Model, width int) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) != m.height {
		t.Errorf("%s: rendered %d lines for a %d-row terminal", describeFocus(m), len(lines), m.height)
		return
	}
	want := lipgloss.Width(lines[0])
	for i, line := range lines {
		got := lipgloss.Width(line)
		if got != want {
			t.Errorf("%s: line %d is %d cells but line 0 is %d: %q",
				describeFocus(m), i, got, want, line)
			break
		}
	}
	if want > width {
		t.Errorf("%s: frame is %d cells wide in a %d-cell terminal", describeFocus(m), want, width)
	}
}

// cellAt returns the visible glyph at cell offset x of a styled line.
func cellAt(line string, x int) string {
	col := 0
	for _, r := range ansi.Strip(line) {
		w := ansi.StringWidth(string(r))
		switch {
		case col == x:
			return string(r)
		case col+w > x:
			return ""
		}
		col += w
	}
	return ""
}

// assertPanelBoxes is the layout invariant: every panel draws its own complete
// rounded box. Panels do not share edges, so each one owns all four corners.
func assertPanelBoxes(t *testing.T, m *Model, width int) {
	t.Helper()
	assertFits(t, m, width)

	view := m.View()
	lines := strings.Split(view, "\n")
	frame := m.glyphs.Border
	// The frame sits below the header, so frame row y is view line y+headerHeight.
	row := func(y int) string {
		index := y + headerHeight
		if index < 0 || index >= len(lines) {
			return ""
		}
		return lines[index]
	}

	for _, slot := range m.panels {
		r := slot.rect
		if r.w < 4 || r.h < 3 {
			continue
		}
		corners := []struct {
			x, y int
			want string
		}{
			{r.x, r.y, frame.TopLeft},
			{r.x + r.w - 1, r.y, frame.TopRight},
			{r.x, r.y + r.h - 1, frame.BottomLeft},
			{r.x + r.w - 1, r.y + r.h - 1, frame.BottomRight},
		}
		for _, corner := range corners {
			if got := cellAt(row(corner.y), corner.x); got != corner.want {
				t.Errorf("%s: panel %q corner at (%d,%d) is %q, want %q",
					describeFocus(m), slot.title, corner.x, corner.y, got, corner.want)
			}
		}
	}

	// Every panel draws its own border. The count is a lower bound rather than an
	// equality because a panel's *content* may draw bordered cards of its own --
	// the day's period grid does exactly that -- and those are content, not
	// frames. The per-panel corner checks above are what prove the frames do not
	// share edges.
	if got := strings.Count(view, frame.TopLeft); got < len(m.panels) {
		t.Errorf("%s: %d panels drew only %d top-left corners",
			describeFocus(m), len(m.panels), got)
	}
}

// overlayName names a utility view for test messages.
func overlayName(o Overlay) string {
	switch o {
	case OverlayLogs:
		return "logs"
	case OverlayForm:
		return "form"
	}
	return "none"
}

// everyView returns each layout the model can draw: every left panel with the
// keyboard in each level, the login content, and the utility views.
func everyView() []struct {
	name  string
	apply func(m *Model)
} {
	type view struct {
		name  string
		apply func(m *Model)
	}
	views := []view{
		{"login", func(m *Model) {
			m.session.phase = phaseIdle
			m.focusSide(PaneRooms)
			m.zone = ZoneMain
		}},
	}
	for _, side := range sidePanes {
		side := side
		views = append(views, view{side.title() + "-列表", func(m *Model) { m.focusSide(side) }})
		views = append(views, view{side.title() + "-内容", func(m *Model) {
			m.focusSide(side)
			m.zone = ZoneMain
		}})
	}
	views = append(views, view{"选座预览", func(m *Model) { m.zone = ZonePreview }})
	for _, overlay := range []Overlay{OverlayLogs, OverlayForm} {
		o := overlay
		views = append(views, view{overlayName(o), func(m *Model) { m.openOverlay(o) }})
	}
	out := make([]struct {
		name  string
		apply func(m *Model)
	}, 0, len(views))
	for _, v := range views {
		out = append(out, struct {
			name  string
			apply func(m *Model)
		}{v.name, v.apply})
	}
	return out
}

func TestFramesStayAlignedEverywhere(t *testing.T) {
	for _, size := range testSizes {
		for _, populated := range []bool{false, true} {
			for _, view := range everyView() {
				m := newTestModel(t)
				resize(t, m, size.w, size.h)
				if populated {
					populate(m)
				}
				view.apply(m)
				m.layout()
				assertPanelBoxes(t, m, size.w)
			}
		}
	}
}

func TestOverlaysFitAtEverySize(t *testing.T) {
	for _, size := range testSizes {
		m := newTestModel(t)
		populate(m)
		resize(t, m, size.w, size.h)
		m.layout()

		m.overlay = OverlayHelp
		assertFits(t, m, size.w)

		m.overlay = OverlayConfirm
		m.confirm = confirmState{
			prompt:  "退出并放弃当前选座预览？",
			detail:  "已准备 024 号座位（未提交）。退出不会发送任何预约请求。",
			confirm: "退出",
			action:  confirmQuit,
		}
		assertFits(t, m, size.w)

		// The utility views take over the frame, so they must fit it too.
		for _, overlay := range []Overlay{OverlayLogs, OverlayForm} {
			m.closeOverlay()
			m.openOverlay(overlay)
			assertFits(t, m, size.w)
		}
	}
}

func TestTooSmallTerminalIsHandled(t *testing.T) {
	m := newTestModel(t)
	resize(t, m, minUsableWidth-1, minUsableHeight-1)
	view := m.View()
	if !strings.Contains(view, "太小") {
		t.Fatalf("expected a minimum-size notice, got %q", view)
	}
}

// A missing session must produce real empty states, never fabricated data.
func TestEmptyStatesAreHonest(t *testing.T) {
	cases := []struct {
		name  string
		setup func(m *Model)
		want  string
	}{
		{"login content", func(m *Model) {
			m.focusSide(PaneRooms)
			m.zone = ZoneMain
		}, "尚未登录"},
		{"room panel", func(m *Model) { m.focusSide(PaneRooms) }, "还没有房间数据"},
		{"room content", func(m *Model) {
			m.session.phase = phaseConfirmed
			m.focusSide(PaneRooms)
			m.zone = ZoneMain
		}, "没有选中的房间"},
		{"period panel", func(m *Model) {
			populate(m)
			m.focusSide(PanePeriods)
		}, "今天"},
		{"seat panel", func(m *Model) {
			m.session.phase = phaseConfirmed
			m.focusSide(PaneSeats)
		}, "只读 · 未选择自习室"},
		{"preview card", func(m *Model) { m.zone = ZonePreview }, "按 enter 生成二维码登录"},
		{"diagnostics", func(m *Model) {
			// Set the view directly: opening it also starts a load, and the
			// empty state is what is under test here.
			m.overlay = OverlayLogs
			m.overlayFocus = 0
		}, "还没有请求日志"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t)
			resize(t, m, 120, 40)
			tc.setup(m)
			m.layout()
			if view := m.View(); !strings.Contains(view, tc.want) {
				t.Errorf("%s empty state is missing %q:\n%s", tc.name, tc.want, view)
			}
		})
	}
}

// Errors are classified, and only the short form reaches the main view.
func TestErrorViewShowsShortFormNotDump(t *testing.T) {
	m := newTestModel(t)
	resize(t, m, 120, 40)
	m.focusSide(PaneRooms)
	m.layout()
	m.rooms.err = classify(&chaoxing.BlockedSeatRequest{Reason: "已在发送前拦截非只读座位接口"})

	view := m.View()
	if !strings.Contains(view, "只读策略已拦截该请求") {
		t.Errorf("policy error not surfaced:\n%s", view)
	}
	// The panel is narrow, so the hint is clipped; what matters is that a
	// recovery hint is rendered at all rather than a raw dump.
	if !strings.Contains(view, "本项目不发送预约请求") {
		t.Errorf("recovery hint missing:\n%s", view)
	}
	if strings.Contains(view, "BlockedSeatRequest") {
		t.Errorf("the raw error type leaked into the view:\n%s", view)
	}
}

func TestConfirmedSessionIsNotReportedAsDisconnected(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	resize(t, m, 120, 40)
	m.focusSide(PaneSeats)
	m.layout()

	view := m.View()
	if !strings.Contains(view, "已登录") {
		t.Errorf("confirmed session not shown as logged in:\n%s", view)
	}
	if strings.Contains(view, "尚未登录") {
		t.Errorf("confirmed session wrongly shown as not logged in:\n%s", view)
	}
}

func TestQuitConfirmsOnlyWhenASelectionExists(t *testing.T) {
	m := newTestModel(t)
	resize(t, m, 120, 40)

	// No selection: quit immediately.
	if _, cmd := m.requestQuit(); cmd == nil {
		t.Fatal("expected a quit command when nothing would be lost")
	}

	populate(m)
	model, cmd := m.requestQuit()
	if cmd != nil {
		t.Fatal("expected no immediate quit while a selection is prepared")
	}
	updated := model.(*Model)
	if updated.overlay != OverlayConfirm {
		t.Fatal("expected a confirmation overlay")
	}
	if updated.confirm.focusYes {
		t.Fatal("focus must start on the safe choice")
	}
}

// The real login code is a 45-module symbol (53x27 cells once a quiet zone is
// added). It has to render rather than fall back to the size notice.
func TestLargeQRRendersOnAStandardTerminal(t *testing.T) {
	m := newTestModel(t)
	resize(t, m, 120, 45)
	m.session.phase = phaseWaiting
	m.focusSide(PaneRooms)
	m.zone = ZoneMain
	m.session.deadline = time.Now().Add(90 * time.Second)
	m.session.qrGrid = make([][]bool, 45)
	for i := range m.session.qrGrid {
		m.session.qrGrid[i] = make([]bool, 45)
	}
	m.layout()

	view := m.View()
	if strings.Contains(view, "终端尺寸不足以显示二维码") {
		t.Fatalf("a 45-module code must fit a 120x45 terminal:\n%s", view)
	}
	if !strings.Contains(view, "剩余") {
		t.Errorf("the countdown hint is missing:\n%s", view)
	}
	if !strings.Contains(view, "▀") {
		t.Error("the QR block was not rendered")
	}
	assertPanelBoxes(t, m, 120)
}

func TestQRNoticeWhenTerminalCanNotFitIt(t *testing.T) {
	m := newTestModel(t)
	m.cfg.ASCIIOnly = true
	m.glyphs = newGlyphs(true)
	resize(t, m, 40, 22)
	m.session.phase = phaseWaiting
	m.focusSide(PaneRooms)
	m.zone = ZoneMain
	m.session.qrGrid = make([][]bool, 45)
	for i := range m.session.qrGrid {
		m.session.qrGrid[i] = make([]bool, 45)
	}
	m.layout()

	view := m.View()
	if !strings.Contains(view, "终端尺寸不足") {
		t.Fatalf("expected an explicit size notice:\n%s", view)
	}
}

// The panel group and the content beside it are on screen at once, in the order
// the keyboard walks them.
func TestAllPanelsAreOnScreenAtOnce(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	resize(t, m, 120, 40)
	focusPanel(m, PaneSeats)

	view := m.View()
	for _, want := range []string{"[0] 账号", "[1] 房间", "[2] 时段", "[3] 座位", "[4] 预订记录", "座位详情", "[5] 选座预览"} {
		if !strings.Contains(view, want) {
			t.Errorf("the frame is missing the %q panel:\n%s", want, view)
		}
	}
	titles := []string{"[0] 账号", "[1] 房间", "[2] 时段", "[3] 座位", "[4] 预订记录", "座位详情", "[5] 选座预览"}
	if len(m.panels) != len(titles) {
		t.Fatalf("the frame drew %d panels, want %d", len(m.panels), len(titles))
	}
	for i, want := range titles {
		if m.panels[i].title != want {
			t.Errorf("panel %d is %q, want %q", i, m.panels[i].title, want)
		}
	}

	// The left column is the panel group: the panels are stacked in it and share
	// its width, and the content is the wide column beside them.
	status, rooms, periods, seats, records := m.panels[0], m.panels[1], m.panels[2], m.panels[3], m.panels[4]
	content, preview := m.panels[5], m.panels[6]
	group := []panelSlot{status, rooms, periods, seats, records}
	for i := 1; i < len(group); i++ {
		if group[i].rect.x != group[0].rect.x {
			t.Errorf("the panel group must be one column: %+v", group)
			break
		}
		if group[i-1].rect.y+group[i-1].rect.h != group[i].rect.y {
			t.Errorf("the panel group must be stacked: %+v", group)
			break
		}
	}
	if content.rect.x <= rooms.rect.x+rooms.rect.w-1 {
		t.Errorf("the content overlaps the panel group: %+v %+v", content.rect, rooms.rect)
	}
	if content.rect.w <= rooms.rect.w || preview.rect.w != content.rect.w {
		t.Errorf("the content must be the wide column: %+v %+v", content.rect, preview.rect)
	}
	if content.rect.y+content.rect.h != preview.rect.y {
		t.Errorf("the preview card must sit under the content: %+v %+v", content.rect, preview.rect)
	}

	// The content follows the active panel: each list gets its own card.
	for _, tc := range []struct {
		side Pane
		want string
	}{
		{PaneRooms, "房间详情"},
		{PanePeriods, "时段详情"},
		{PaneSeats, "座位详情"},
	} {
		focusPanel(m, tc.side)
		main := m.panels[len(m.panels)-2]
		if main.title != tc.want {
			t.Errorf("the content of %v is %q, want %q", tc.side.title(), main.title, tc.want)
		}
	}

	// A taller terminal grows the panel group.
	tall := newTestModel(t)
	populate(tall)
	resize(t, tall, 120, 60)
	if tall.panels[3].rect.h <= seats.rect.h {
		t.Errorf("a taller terminal must grow the panel group: %d vs %d",
			tall.panels[3].rect.h, seats.rect.h)
	}
}

// Before a session exists the content is the login panel, and after login the same
// slot becomes the active panel's content.
func TestContentFollowsTheSession(t *testing.T) {
	m := newTestModel(t)
	resize(t, m, 120, 40)
	focusPanel(m, PaneStatus)
	m.zone = ZoneMain
	m.layout()

	view := m.View()
	if !strings.Contains(view, "尚未登录") {
		t.Errorf("the content does not offer the login state:\n%s", view)
	}
	// The panel group is still there: only the content changes.
	for _, want := range []string{"[0] 账号", "[1] 房间", "[2] 时段", "[3] 座位"} {
		if !strings.Contains(view, want) {
			t.Errorf("the panel group lost %q before login:\n%s", want, view)
		}
	}

	// With a session, the project panel's content becomes the project report, and
	// the room panel's content is the room's detail.
	populate(m)
	m.layout()
	view = m.View()
	if strings.Contains(view, "尚未登录") {
		t.Errorf("the content did not turn into the panel's own content:\n%s", view)
	}
	if !strings.Contains(view, "账号详情") {
		t.Errorf("the project panel's content is not the project report:\n%s", view)
	}
	focusPanel(m, PaneRooms)
	m.zone = ZoneMain
	m.layout()
	if view := m.View(); !strings.Contains(view, "房间详情") {
		t.Errorf("the room panel's content is not the room detail:\n%s", view)
	}
}

// The panel group is wide, and it narrows in steps instead of squeezing the
// content out of the frame: the right column must stay usable at every width.
func TestPanelGroupWidthSteps(t *testing.T) {
	for _, tc := range []struct {
		width int
		left  int
	}{
		{200, leftPanelWidthWide},
		{120, leftPanelWidthWide},
		{100, leftPanelWidthMid},
		{80, leftPanelWidthMid},
		{70, leftPanelWidthNarrow},
		{minUsableWidth, leftPanelWidthNarrow},
	} {
		m := newTestModel(t)
		populate(m)
		resize(t, m, tc.width, 30)
		focusPanel(m, PaneSeats)

		if got := m.leftColumnWidth(); got != tc.left {
			t.Errorf("at %d columns the panel group is %d cells, want %d", tc.width, got, tc.left)
		}
		var group, content *panelSlot
		for i := range m.panels {
			switch m.panels[i].owner.zone {
			case ZoneSide:
				group = &m.panels[i]
			case ZoneMain:
				content = &m.panels[i]
			}
		}
		if group == nil || content == nil {
			t.Fatalf("at %d columns the frame is missing a column", tc.width)
		}
		if content.rect.w != tc.width-group.rect.w {
			t.Errorf("at %d columns the content is %d cells, want %d",
				tc.width, content.rect.w, tc.width-group.rect.w)
		}
		// A content column too narrow to read anything is not usable, so the
		// narrow step has to leave room for the right column.
		if content.interiorW < 12 {
			t.Errorf("at %d columns the content is only %d cells wide", tc.width, content.interiorW)
		}
	}
}

// The seat's report carries how it stands in the other periods that have been
// queried. That is where the information lives now that the list is static, and it
// is the same data the details card gives for one seat.
func TestSeatReportCarriesTheOtherPeriods(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	second := testOccupancy()
	second.Start, second.End = "21:00", "21:30"
	m.room.slots = append(m.room.slots, chaoxingSlotAt("21:00", "21:30"))
	m.room.remember(second)
	resize(t, m, 120, 34)
	focusPanel(m, PaneSeats)
	focusZone(m, ZoneMain)

	rows := strings.Join(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneMain
	}), "\n")
	if !strings.Contains(rows, "21:00") {
		t.Errorf("the seat detail does not carry the other period's state:\n%s", rows)
	}
	if !strings.Contains(rows, "位置") {
		t.Errorf("the seat map does not carry a position:\n%s", rows)
	}
	if !strings.Contains(rows, "001") {
		t.Errorf("the seat map does not draw the seats:\n%s", rows)
	}
}

// 80 columns is the width that must remain fully usable, for every view.
func TestEightyColumnsRemainsUsable(t *testing.T) {
	for _, view := range everyView() {
		m := newTestModel(t)
		populate(m)
		resize(t, m, 80, 24)
		view.apply(m)
		m.layout()
		assertPanelBoxes(t, m, 80)

		rendered := m.View()
		if strings.Contains(rendered, "终端窗口太小") {
			t.Fatalf("%s: 80x24 must be usable", view.name)
		}
		if !strings.Contains(rendered, "帮助") {
			t.Errorf("%s: the context help bar is missing at 80 columns", view.name)
		}
	}
}
