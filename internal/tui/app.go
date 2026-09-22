package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"wfuseat/internal/api"
	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
	"wfuseat/internal/storage"
)

// Pane identifies one keyboard-owning region of the frame. The order is the
// visual order, and it is linear by design: the left column stacks two panels, so
// "next panel" has to be a single well-defined sequence rather than a pair of
// horizontal neighbours.
//
// There is no page concept: every panel is on screen at once, and the digit in a
// panel's title is the key that focuses it.
type Pane int

const (
	// PaneStatus is the project panel: what the tool is, and the QR login that
	// starts every session.
	PaneStatus Pane = iota
	PaneRooms
	PanePeriods
	PaneSeats
	// PaneRecords lists what has been booked, and what this session has prepared.
	PaneRecords
	PaneDetails
	paneCount
)

var paneTitles = [...]string{"账号", "房间", "时段", "座位", "预订记录", "选座预览"}

func (p Pane) title() string {
	if int(p) < 0 || int(p) >= len(paneTitles) {
		return ""
	}
	return paneTitles[p]
}

// Overlay is a modal layer above the frame. Help and Confirm are questions, so
// they replace the screen with a centred box; Logs, Settings and Form are whole
// views and are rendered through the same panel frame as everything else.
type Overlay int

const (
	OverlayNone Overlay = iota
	OverlayHelp
	OverlayConfirm
	OverlayLogs
	OverlayForm
)

// frameOverlay reports whether this overlay renders through the panel frame.
func (o Overlay) frameOverlay() bool {
	switch o {
	case OverlayLogs, OverlayForm:
		return true
	}
	return false
}

// Model is the whole application state. Screen-specific state lives in its own
// struct so no single file becomes the dumping ground.
type Model struct {
	remote           *api.Client
	qrWindowCancel   context.CancelFunc
	qrWindowDone     bool
	qrWindowErr      error
	qrWindowLauncher func(context.Context, [][]bool) error

	addAccountReturn   *Model
	closed             bool
	db                 jobStore
	restoreSession     bool
	candidates         []string
	jobs               []storage.Job
	jobsLoading        bool
	pendingJob         *storage.Job
	pendingCancelJobID string
	width, height      int
	ready              bool

	cfg     config.Config
	cfgPath string

	login  *chaoxing.QRLogin
	client *chaoxing.SeatClient

	session               sessionState
	rooms                 roomsState
	room                  roomState
	logs                  logsState
	settings              settingsState
	repeatEnabled         bool
	repeatMode            int
	repeatModeCursor      int
	repeatStart           string
	repeatWeek            [7]storage.DayPlan
	repeatWeekCursor      int
	repeatCopy            int
	repeatCommon          storage.DayPlan
	repeatWeekInitialized bool
	repeatInitialized     bool
	repeatFocus           int // 0 intervals, 1 rule, 2 start date, 4 weekdays, 5 copy
	repeatEditing         bool
	repeatInput           textinput.Model
	accountList           listView

	selection *chaoxing.Selection
	write     writeState
	auto      autoState
	days      dayState
	preorders preordersState

	// gridCols is how many cells the content grid last fitted, so the movement
	// keys and the renderer agree about the geometry.
	gridCols    int
	periodWidth int

	// form renders the prepared signature form in its own view.
	form viewport.Model

	// panels is the current frame geometry, rebuilt by layout() so the frame and
	// the widgets it sizes always agree about how much room exists.
	panels []panelSlot

	filter filterState

	// side is the active list of the left panel group: h/l switch it and j/k
	// moves inside it. zone says which level of the frame owns the keyboard: the
	// side list, the content of that list, or the prepared selection below it.
	// overlayFocus is the same idea inside a utility view.
	side         Pane
	zone         Zone
	overlayFocus int
	zoneBefore   Zone

	overlay Overlay
	confirm confirmState
	helpTop int

	theme  Theme
	glyphs Glyphs
	keys   KeyMap

	spinner spinner.Model
	toasts  []toast

	ctx      context.Context
	cancel   context.CancelFunc
	quitSkip bool

	// Generation counters. A reply tagged with an older generation is discarded,
	// so a slow response cannot overwrite state the user has already moved past.
	genSession int
	genRooms   int
	genRoom    int
	genSeats   int
	genLogs    int
	genScan    int
	genWrite   int
	genSelect  int
}

// Option customises construction.
type Option func(*Model)

// WithSession supplies the session to drive instead of creating one from the
// configuration. It exists so an injected transport can exercise the whole
// workflow without real network I/O.
func WithSession(login *chaoxing.QRLogin) Option {
	return func(m *Model) { m.login = login }
}

// New builds the application. It performs no network I/O: the first request only
// happens when the user asks for it, so a misconfigured proxy shows up as a
// clear state rather than a surprise failure on launch.
func New(cfg config.Config, cfgPath string, opts ...Option) (*Model, error) {
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())

	m := &Model{
		cfg:     cfg,
		cfgPath: cfgPath,
		ctx:     ctx,
		cancel:  cancel,
		theme:   newTheme(),
		glyphs:  newGlyphs(cfg.ASCIIOnly),
		keys:    newKeyMap(),
		// The keyboard starts on the status panel: that is where the login lives,
		// so enter there is the first step of the workflow. Logging in moves the
		// keyboard to the room list.
		side: PaneStatus,
		zone: ZoneSide,
	}
	for _, opt := range opts {
		opt(m)
	}
	if m.login == nil {
		login, err := chaoxing.NewQRLogin(cfg.Proxy, cfg.LogDir)
		if err != nil {
			cancel()
			return nil, err
		}
		m.login = login
	}
	if cfg.UserAgent != "" {
		m.login.Session.UserAgent = cfg.UserAgent
	}
	m.client = chaoxing.NewSeatClient(m.login, cfg.FIDEnc, cfg.MappID)
	m.client.SetReserveConfig(reserveConfig(cfg))
	if m.remote != nil {
		m.client.Remote = m.remote
	}
	m.spinner = spinner.New(
		spinner.WithSpinner(m.glyphs.Spinner),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(m.theme.Primary)),
	)
	m.logs.detail = viewport.New(0, 0)
	m.form = viewport.New(0, 0)
	m.filter.input = textinput.New()
	m.filter.input.Placeholder = "过滤…"
	m.filter.input.Prompt = "/ "
	m.filter.input.CharLimit = 64
	m.settings = newSettingsState(cfg)
	m.preorders.path = filepath.Join(filepath.Dir(cfg.LogDir), "preorders.json")
	if cfg.Day != "" {
		m.rooms.day = cfg.Day
	}
	if err := m.initAccount(); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

// Close releases resources. It is safe to call more than once.
func (m *Model) Close() {
	if m.closed {
		return
	}
	m.closed = true
	if m.remote != nil {
		m.remote.CancelLoginAsync()
	}
	m.closeQRWindow()
	if m.cancel != nil {
		m.cancel()
	}
	if m.login != nil {
		_ = m.login.Close()
	}
	if m.db != nil {
		m.db.Close()
	}
	if strings.HasPrefix(m.cfg.Account, "pending-") {
		dir := filepath.Join(m.cfg.Root, "accounts", m.cfg.Account)
		if _, e := os.Lstat(dir); e == nil {
			_ = storage.RemoveAccount(m.cfg.Root, m.cfg.Account)
		}
	}

	if m.addAccountReturn != nil {
		m.addAccountReturn.Close()
		m.addAccountReturn = nil
	}

}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick, tickCmd(m.cfg.Account)}
	if m.db != nil && (m.remote == nil || m.remote.Authorized()) {
		cmds = append(cmds, m.cmdJobs())
		if m.restoreSession {
			cmds = append(cmds, m.beginOpenHome())
		}
	}
	return tea.Batch(cmds...)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case remoteDeletedMsg:
		return m.onRemoteDeleted(msg)
	case qrWindowClosedMsg:
		return m.onQRWindowClosed(msg)
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		return m, m.ensureQRWindow()

	case tea.KeyMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tickMsg:
		if msg.account != m.cfg.Account {
			return m, nil
		}
		m.expireToasts(msg.at)
		if m.db != nil && !m.jobsLoading && (m.remote == nil || m.remote.Authorized()) {
			m.jobsLoading = true
			return m, tea.Batch(tickCmd(m.cfg.Account), m.cmdJobs())
		}
		if shot, due := m.dueAutoReserve(msg.at); due {
			cmd := m.fireAutoReserve(shot)
			return m, tea.Batch(tickCmd(m.cfg.Account), cmd)
		}
		return m, tickCmd(m.cfg.Account)
	}

	return m.handleAsync(msg)
}

// layout rebuilds the frame geometry for the active view and sizes the widgets
// from it. Everything size-dependent happens here rather than during rendering,
// so View stays a projection of state.
func (m *Model) layout() {
	width, height := m.frameSize()
	m.panels = m.layoutFrame(width, height, m.frameColumns())
	m.syncLists()
}

// frameColumns describes the panel frame. The main view is the selection chain the
// user walks with enter: the status card, the room list and the period list stacked
// in the left column, with the seat list and the details panel filling the right one.
// The utility views reuse the same machinery rather than a second renderer.
func (m *Model) frameColumns() []columnSpec {
	switch m.overlay {
	case OverlayLogs:
		return m.logsColumns()
	case OverlayForm:
		return []columnSpec{{rows: []rowSpec{{slot: m.formPanel()}}}}
	}
	left := columnSpec{width: m.leftColumnWidth(), rows: m.leftColumnRows()}
	right := columnSpec{rows: []rowSpec{
		{slot: m.mainPanel()},
		{height: m.detailPanelBodyRows(), slot: m.previewPanel()},
	}}
	return []columnSpec{left, right}
}

// leftColumnRows is the panel group: one panel per area, with the project panel
// on top. Every panel keeps the same size whether or not it is focused, so the
// column never reflows as h/l move through it.
func (m *Model) leftColumnRows() []rowSpec {
	rows := make([]rowSpec, 0, len(sidePanes))
	for _, side := range sidePanes {
		rows = append(rows, rowSpec{slot: m.sidePanel(side)})
	}
	return rows
}

// detailPanelBodyRows keeps the preview card big enough for the whole selection
// summary, plus a row for the service's answer to the last write. With nothing
// prepared it shrinks, because the rows are worth more to the content beside it
// -- on a short terminal that is the difference between a readable login code and
// a size notice.
func (m *Model) detailPanelBodyRows() int {
	if m.selection == nil && m.write.result == nil {
		if _, height := m.frameSize(); height < statusPanelStatusMinRows {
			return detailPanelBodyRowsEmpty
		}
		return detailPanelBodyRowsEmpty + 1
	}
	if _, height := m.frameSize(); height < statusPanelStatusMinRows {
		return detailPanelBodyRows - 1
	}
	return detailPanelBodyRows
}

// leftColumnWidth is the preferred interior width of the panel group.
func (m *Model) leftColumnWidth() int {
	switch {
	case m.width >= wideBreakpoint:
		return leftPanelWidthWide
	case m.width >= leftPanelBreakpointMid:
		return leftPanelWidthMid
	default:
		return leftPanelWidthNarrow
	}
}

// View renders the whole application.
func (m *Model) View() string {
	if !m.ready {
		return ""
	}
	if m.width < minUsableWidth || m.height < minUsableHeight {
		return m.tooSmallView()
	}
	if m.overlay != OverlayNone && !m.overlay.frameOverlay() {
		// A question replaces the screen: a terminal has no z-buffer, so
		// compositing over live content would smear it.
		return m.constrain(m.overlayView(), m.width, m.height)
	}
	_, height := m.frameSize()
	return m.constrain(lipgloss.JoinVertical(lipgloss.Left,
		m.headerView(),
		m.renderFrame(m.width, height, m.panels),
		m.footerView(),
	), m.width, m.height)
}

func (m *Model) tooSmallView() string {
	msg := lipgloss.JoinVertical(lipgloss.Center,
		m.theme.Heading.Render("终端窗口太小"),
		m.theme.MutedText.Render(fmt.Sprintf("当前 %d×%d，至少需要 %d×%d",
			m.width, m.height, minUsableWidth, minUsableHeight)),
	)
	return lipgloss.Place(max(m.width, 1), max(m.height, 1), lipgloss.Center, lipgloss.Center, msg)
}

// itoa is a local shorthand for decimal formatting.
func itoa(v int) string { return strconv.Itoa(v) }

// viewTitle names the current view for the header.
func (m *Model) viewTitle() string {
	switch m.overlay {
	case OverlayLogs:
		return "诊断"
	case OverlayForm:
		return "表单"
	}
	if m.session.phase != phaseConfirmed {
		if m.cfg.Account != "" {
			return "账号 · " + m.accountLabel()
		}
		return "会话"
	}
	return m.side.title() + func() string {
		if m.remote != nil {
			return " · " + m.accountLabel() + " · 远程 " + m.remote.BaseURL
		}
		if m.cfg.Account != "" {
			return " · 账号 " + m.accountLabel()
		}
		return ""
	}()
}

// handleKey routes a key press. Overlays and inline editors get priority, so a
// global binding can never fire while the user is typing -- that is what keeps q
// from quitting while a filter or a setting is being edited.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Force quit is always available, even mid-request or mid-edit.
	if msg.Type == tea.KeyCtrlC {
		m.cancel()
		return m, tea.Quit
	}
	switch m.overlay {
	case OverlayHelp:
		return m.handleHelpKey(msg)
	case OverlayConfirm:
		return m.handleConfirmKey(msg)
	}
	if m.filter.active {
		return m.handleFilterKey(msg)
	}
	if m.settings.editing {
		return m.handleSettingsEditKey(msg)
	}

	if m.repeatEditing {
		return m.editRepeatStart(msg)
	}

	if msg.String() == "u" {
		m.focusSide(PaneStatus)
		m.zone = ZoneMain
		return m, nil
	}
	if m.overlay == OverlayNone && m.side == PanePeriods && m.zone == ZoneMain && m.autoPreview() {
		if m.repeatMode == 2 && (m.repeatFocus == 0 || m.repeatFocus == 4) {
			switch msg.String() {
			case "h", "left":
				m.repeatFocus = 4
				return m, nil
			case "l", "right":
				m.repeatFocus = 0
				return m, nil
			}
		}
		if msg.Type == tea.KeyTab || msg.Type == tea.KeyShiftTab {
			m.cycleAutoFocus(msg.Type == tea.KeyShiftTab)
			return m, nil
		}
		if msg.Type == tea.KeyEsc && m.repeatFocus != 0 {
			m.repeatFocus = 0
			return m, nil
		}
		if m.repeatFocus != 0 && isAutoControlKey(msg) {
			return m.keyRepeatOptions(msg)
		}
	}

	if m.overlay == OverlayNone && m.side == PaneStatus {
		if handled, model, cmd := m.accountControl(msg); handled {
			return model, cmd
		}
		if m.zone == ZoneMain && m.session.phase == phaseConfirmed {
			if msg.Type == tea.KeyEsc {
				m.settings.draft = m.cfg
				m.settings.dirty = false
				m.settings.err = nil
			} else if isSettingsKey(msg) {
				return m.keySettings(msg)
			}
		}
	}

	// Panel movement. h/l choose the active panel while the keyboard is in the
	// panel group; in the right column they belong to whatever grid is there, so
	// they fall through to the panel's own keys and esc is the way back out.
	switch {
	case key.Matches(msg, m.keys.Left):
		if m.sideways() {
			m.stepLeft()
			return m, nil
		}
	case key.Matches(msg, m.keys.Right):
		if m.sideways() {
			m.stepRight()
			return m, nil
		}
	}
	switch {
	case key.Matches(msg, m.keys.Auto):
		return m.beginAutoReserve()
	case key.Matches(msg, m.keys.DayNext):
		return m.moveDayCursor(1)
	case key.Matches(msg, m.keys.DayPrev):
		return m.moveDayCursor(-1)
	case key.Matches(msg, m.keys.DayToday):
		return m.resetDayCursor()
	}

	// Global actions, none of which overlap with movement or activation.
	switch {
	case key.Matches(msg, m.keys.Help):
		m.overlay = OverlayHelp
		m.helpTop = 0
		return m, nil
	case key.Matches(msg, m.keys.Quit):
		return m.requestQuit()
	case key.Matches(msg, m.keys.Back):
		return m.handleBack()
	case key.Matches(msg, m.keys.Refresh):
		return m.refresh()
	case key.Matches(msg, m.keys.NextPanel):
		m.cycleZone(1)
		return m, nil
	case key.Matches(msg, m.keys.PrevPanel):
		m.cycleZone(-1)
		return m, nil
	case key.Matches(msg, m.keys.Logs):
		return m, m.toggleOverlay(OverlayLogs)
	}
	for i, binding := range m.keys.Panes {
		if key.Matches(msg, binding) {
			return m.jumpPanel(Pane(i)), nil
		}
	}
	if key.Matches(msg, m.keys.Preview) {
		m.zone = ZonePreview
		return m, nil
	}
	return m.paneKey(msg)
}

// paneKey routes a key to whichever level currently owns the keyboard.
func (m *Model) paneKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.overlay {
	case OverlayLogs:
		if m.overlayFocus == 1 {
			return m.keyLogDetail(msg)
		}
		return m.keyLogs(msg)
	case OverlayForm:
		return m.keySelection(msg)
	}
	switch m.zone {
	case ZoneMain:
		return m.keyMain(msg)
	case ZonePreview:
		return m.keyPreview(msg)
	}
	switch m.side {
	case PaneStatus:
		return m.keySession(msg)
	case PaneRooms:
		return m.keyRooms(msg)
	case PanePeriods:
		return m.keyPeriods(msg)
	case PaneSeats:
		if m.session.phase != phaseConfirmed {
			return m.keySession(msg)
		}
		return m.keySeats(msg)
	case PaneRecords:
		return m.keyRecords(msg)
	}
	return m, nil
}

// --- focus -----------------------------------------------------------------

// focusSide makes a left panel active and puts the keyboard on it. The content
// beside the panel group belongs to the active panel, so this re-lays the frame:
// the panel's own detail is what the right column now shows.
func (m *Model) focusSide(p Pane) {
	changed := m.side != p
	m.side = p
	m.zone = ZoneSide
	if changed {
		m.layout()
	}
}

// focusZone moves the keyboard into the right column, or back to the side list.
func (m *Model) focusZone(zone Zone) { m.zone = zone }

// jumpPanel is what the digits do: a panel's own number focuses that list, which
// is the number printed in its title.
func (m *Model) jumpPanel(p Pane) tea.Model {
	if int(p) < 0 || int(p) >= len(sidePanes) {
		m.zone = ZonePreview
		return m
	}
	m.focusSide(sidePanes[int(p)])
	return m
}

// sideways reports whether h/l mean "change panel" right now: in the panel group
// they do, inside a utility view they move between the view's panels, and in the
// right column of the main frame they belong to the grid.
func (m *Model) sideways() bool {
	return m.overlay.frameOverlay() || m.zone == ZoneSide
}

// stepLeft is h. It only means something in the panel group, where it walks to the
// previous panel; in the right column the movement keys belong to the grid, and
// esc is what returns to the group.
func (m *Model) stepLeft() {
	if m.overlay.frameOverlay() {
		m.overlayFocus = 0
		return
	}
	if m.zone == ZoneSide {
		m.cycleSide(-1)
	}
}

// stepRight is l, the mirror of stepLeft.
func (m *Model) stepRight() {
	if m.overlay.frameOverlay() {
		m.overlayFocus = m.viewPaneCount() - 1
		return
	}
	if m.zone == ZoneSide {
		m.cycleSide(1)
	}
}

// cycleSide moves h/l through the panel group, wrapping at both ends.
func (m *Model) cycleSide(delta int) {
	total := len(sidePanes)
	m.focusSide(sidePanes[((sideIndex(m.side)+delta)%total+total)%total])
}

// cycleZone walks tab through the levels, wrapping like lazygit's.
func (m *Model) cycleZone(delta int) {
	if m.overlay.frameOverlay() {
		total := m.viewPaneCount()
		if total > 1 {
			m.overlayFocus = ((m.overlayFocus+delta)%total + total) % total
		}
		return
	}
	ring := [...]Zone{ZoneSide, ZoneMain, ZonePreview}
	index := 0
	for i, zone := range ring {
		if zone == m.zone {
			index = i
		}
	}
	m.zone = ring[((index+delta)%len(ring)+len(ring))%len(ring)]
}

// viewPaneCount is how many panels a utility view can focus.
func (m *Model) viewPaneCount() int {
	if m.overlay == OverlayLogs {
		return 2
	}
	return 1
}

// keyMain handles the content beside the panel group. The content itself is a
// report, so its only action is to run the active panel's action again -- which
// is how a refresh reads from inside the content.
func (m *Model) keyMain(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The project panel's content is the login, and before a session exists that
	// is the only content worth pressing enter on.
	if m.side == PaneStatus || m.session.phase != phaseConfirmed {
		return m.keySession(msg)
	}
	// The content of these panels is a picker or a report. space commits what the
	// cursor is on and nothing else -- it never moves the keyboard on; enter is what
	// steps to the next item and shows its preview straight away. The movement keys
	// belong to the grid, which is why h does not leave the panel and esc does.
	switch m.side {
	case PaneSeats:
		return m.keySeatGrid(msg)
	case PanePeriods:
		return m.keyPeriodGrid(msg)
	case PaneRecords:
		return m.keyRecords(msg)
	}
	switch {
	case key.Matches(msg, m.keys.Space):
		return m.selectSide()
	case key.Matches(msg, m.keys.Enter):
		return m.advanceChain(nextPane(m.side))
	}
	return m, nil
}

// nextPane is the panel after this one in the selection chain.
func nextPane(p Pane) Pane {
	for i, side := range sidePanes {
		if side == p && i+1 < len(sidePanes) {
			return sidePanes[i+1]
		}
	}
	return PaneRecords
}

// advanceChain is what enter does: step to the next item and show its preview --
// the room's periods, the day's periods, the seat map, the prepared selection.
func (m *Model) advanceChain(next Pane) (tea.Model, tea.Cmd) {
	switch next {
	case PaneRecords:
		m.focusSide(PaneRecords)
		m.focusZone(ZoneMain)
		return m, m.cmdLoadPreorders()
	}
	if next == PaneDetails {
		m.focusZone(ZonePreview)
		return m, nil
	}
	m.focusSide(next)
	m.focusZone(ZoneMain)
	return m, nil
}

// moveInGrid applies one step of grid movement: h/l move along a row and j/k
// between rows, which is what a grid needs and why the panel uses esc to leave.
func (m *Model) moveInGrid(deltaRows, deltaCols int, count int, cursor *int, set func(int)) {
	columns := max(m.gridCols, 1)
	index := *cursor
	row, col := index/columns, index%columns
	row += deltaRows
	col += deltaCols
	if row < 0 || row*columns >= count {
		return
	}
	next := row*columns + col
	if col < 0 || col >= columns || next < 0 || next >= count {
		return
	}
	set(next)
}

// keySeatGrid moves through the seat map. j/k change row, h/l change seat within
// the row, enter queries the whole period (which is what fills the map), and esc
// returns to the panel group.
func (m *Model) keySeatGrid(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	numbers := m.visibleSeatNumbers()
	// With the service's coordinates the movement is spatial -- along the seat's row
	// and to the nearest seat in the next one -- so the cursor travels the way the
	// map reads rather than the way the list happens to be numbered.
	plan := m.seatPlan()
	switch {
	case key.Matches(msg, m.keys.Left):
		if plan != nil {
			m.moveSeatPlan(plan, 0, -1)
			return m, nil
		}
		m.moveInGrid(0, -1, len(numbers), &m.room.seats.cursor, m.seatCursorSet)
		return m, nil
	case key.Matches(msg, m.keys.Right):
		if plan != nil {
			m.moveSeatPlan(plan, 0, 1)
			return m, nil
		}
		m.moveInGrid(0, 1, len(numbers), &m.room.seats.cursor, m.seatCursorSet)
		return m, nil
	case key.Matches(msg, m.keys.Up):
		if plan != nil {
			m.moveSeatPlan(plan, -1, 0)
			return m, nil
		}
		m.moveInGrid(-1, 0, len(numbers), &m.room.seats.cursor, m.seatCursorSet)
		return m, nil
	case key.Matches(msg, m.keys.Down):
		if plan != nil {
			m.moveSeatPlan(plan, 1, 0)
			return m, nil
		}
		m.moveInGrid(1, 0, len(numbers), &m.room.seats.cursor, m.seatCursorSet)
		return m, nil
	case key.Matches(msg, m.keys.Top):
		if plan != nil {
			m.focusSeatNumber(plan.firstSeat())
			return m, nil
		}
		m.seatCursorSet(0)
		return m, nil
	case key.Matches(msg, m.keys.End):
		if plan != nil {
			m.focusSeatNumber(plan.lastSeat())
			return m, nil
		}
		if len(numbers) > 0 {
			m.seatCursorSet(len(numbers) - 1)
		}
		return m, nil
	case key.Matches(msg, m.keys.Space):
		return m.selectSeatFromMap()
	case key.Matches(msg, m.keys.Enter):
		// enter moves on: the prepared selection is the next item.
		return m.advanceChain(PaneDetails)
	}
	return m, nil
}

// selectSeatFromMap commits the seat under the cursor. A seat cannot be judged
// without its period, so when the occupancy is unknown the query is started first
// and the selection is completed by the reply -- one key press either way.
func (m *Model) selectSeatFromMap() (tea.Model, tea.Cmd) {
	if m.db != nil {
		return m.planSeatAction()
	}
	number, ok := m.selectedSeatNumber()
	if !ok {
		m.pushToast(sevInfo, "没有可选择的座位")
		return m, nil
	}
	if _, cached := m.room.currentOccupancy(); !cached {
		m.room.pendingSeat = number
		m.pushToast(sevInfo, "已选中 %s 号 · 正在查询该时段…", number)
		return m, m.querySelectedPeriod()
	}
	m.room.pendingSeat = ""
	return m.planSeatAction()
}

// seatCursorSet moves the seat list cursor and keeps its window on it, so the
// linear list and the map always show the same seat as selected.
func (m *Model) seatCursorSet(index int) {
	count := len(m.visibleSeatNumbers())
	if count == 0 {
		return
	}
	m.room.seats.cursor = min(max(index, 0), count-1)
	m.room.seats.clamp()
	m.layout()
}

// keyPeriodGrid moves through the day's periods in reading order and queries the
// one under the cursor. The grid is tiled for reading, but movement stays linear
// so j/k never stops meaning "move inside this panel".
func (m *Model) keyPeriodGrid(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	count := len(m.room.slots)
	switch {
	case key.Matches(msg, m.keys.Left):
		m.movePeriodColumn(-1)
		return m, nil
	case key.Matches(msg, m.keys.Right):
		m.movePeriodColumn(1)
		return m, nil
	case key.Matches(msg, m.keys.Up):
		m.moveInGrid(-1, 0, count, &m.room.slotIndex, m.selectSlot)
		return m, nil
	case key.Matches(msg, m.keys.Down):
		m.moveInGrid(1, 0, count, &m.room.slotIndex, m.selectSlot)
		return m, nil
	case key.Matches(msg, m.keys.Top):
		m.selectSlot(0)
		return m, nil
	case key.Matches(msg, m.keys.End):
		m.selectSlot(count - 1)
		return m, nil
	case key.Matches(msg, m.keys.PageUp):
		m.moveInGrid(-max(m.frameHeight()-2, 1), 0, count, &m.room.slotIndex, m.selectSlot)
		return m, nil
	case key.Matches(msg, m.keys.PageDown):
		m.moveInGrid(max(m.frameHeight()-2, 1), 0, count, &m.room.slotIndex, m.selectSlot)
		return m, nil
	case key.Matches(msg, m.keys.Scan):
		return m, m.beginScanPeriods()
	case key.Matches(msg, m.keys.Space):
		return m.selectPeriodFromGrid()
	case key.Matches(msg, m.keys.Enter):
		if m.autoPreview() && m.repeatMode == 2 {
			m.nextAutoWeekday()
			return m, nil
		}
		return m.advanceChain(PaneSeats)
	}
	return m, nil
}

// selectPeriodFromGrid toggles an endpoint and queries the entire selected interval.
func (m *Model) selectPeriodFromGrid() (tea.Model, tea.Cmd) {
	if m.autoPreview() {
		m.initAutoDraft()
	}
	if m.db != nil && len(m.room.slots) > 0 {
		old := append([]int(nil), m.room.endpoints...)
		m.room.periodExplicit = true
		found := -1
		for i, v := range m.room.endpoints {
			if v == m.room.slotIndex {
				found = i
			}
		}
		if found >= 0 {
			m.room.endpoints = append(m.room.endpoints[:found], m.room.endpoints[found+1:]...)
		} else {
			if len(m.room.endpoints) == 2 {
				m.pushToast(sevWarn, "最多选择两个端点；空格取消已选端点后重选")
				return m, nil
			}
			m.room.endpoints = append(m.room.endpoints, m.room.slotIndex)
		}
		if _, ok := m.room.currentSlot(); !ok && len(m.room.endpoints) > 0 {
			m.room.endpoints = old
			m.pushToast(sevWarn, "不能跨越不连续或暂停时段")
			return m, nil
		}
		if m.autoPreview() {
			m.storeAutoInterval()
		}
		m.candidates = nil
		m.selection = nil
		m.genSelect++
		m.room.pendingSeat = ""
		if len(m.room.endpoints) == 0 {
			m.pushToast(sevInfo, "已取消时段选择；空格选择一个或两个端点")
			return m, nil
		}
	}

	slot, ok := m.room.currentSlot()
	if !ok {
		m.pushToast(sevWarn, "先在 %s 载入一个日期", paneTitle(PanePeriods))
		return m, nil
	}
	occupancy, cached := m.room.occupancyFor(slot.StartTime, slot.EndTime)
	switch {
	case !cached:
		m.pushToast(sevInfo, "已选定时段 %s · 正在查询…", slot.StartTime+"–"+slot.EndTime)
		return m, m.beginLoadSeats(slot)
	case occupancy.Free == 0:
		m.pushToast(sevWarn, "已选定时段 %s · 已满（%d 座）· 按 3 看座位",
			slot.StartTime+"–"+slot.EndTime, occupancy.Total())
	default:
		m.pushToast(sevOK, "已选定时段 %s · %d 空闲 · 按 3 选座位",
			slot.StartTime+"–"+slot.EndTime, occupancy.Free)
	}
	return m, nil
}

// completePendingSeat turns a selection that was waiting for its period into a
// prepared preview, reporting what the seat turned out to be.
func (m *Model) completePendingSeat(number string) (tea.Model, tea.Cmd) {
	if _, known := m.seatState(number); !known {
		m.pushToast(sevWarn, "%s 号不在该时段的座位表里", number)
		return m, nil
	}
	return m.planSeatAction()
}

// frameHeight is the panel area's height, for page-sized grid hops.
func (m *Model) frameHeight() int {
	_, height := m.frameSize()
	return height
}

// selectSide is space in the panel group: it commits what the cursor is on. The
// work belongs to the panel (open the room, load the day, choose the seat), and the
// next item's information is fetched straight away -- but the keyboard stays where
// it is, because selecting is selecting and walking on is enter's job.
func (m *Model) selectSide() (tea.Model, tea.Cmd) {
	switch m.side {
	case PaneStatus:
		return m.keySession(tea.KeyMsg{Type: tea.KeyEnter})
	case PaneRooms:
		return m.selectRoom()
	case PanePeriods:
		return m.selectPlannerDay()
	case PaneSeats:
		return m.selectSeat()
	case PaneRecords:
		return m.beginCancelPreorder()
	}
	return m, nil
}

// previewSide is enter in the panel group: it opens the content beside the panel
// without committing anything. A step whose data is not loaded yet says so rather
// than fetching behind the user's back.
func (m *Model) previewSide() (tea.Model, tea.Cmd) {
	m.focusZone(ZoneMain)
	return m, nil
}

// selectRoom opens the room under the cursor, which loads its periods; the keyboard
// stays on the room list.
func (m *Model) selectRoom() (tea.Model, tea.Cmd) {
	if m.busyClient() {
		m.pushToast(sevInfo, "请等待当前请求完成")
		return m, nil
	}
	room, ok := m.selectedRoom()
	if !ok {
		if len(m.rooms.all) == 0 {
			m.pushToast(sevWarn, "房间列表为空 · 按 r 重新载入")
		} else {
			m.pushToast(sevInfo, "没有选中的自习室")
		}
		return m, nil
	}
	if !room.Selectable {
		// The service owns this flag; say so rather than pretending the key failed.
		m.pushToast(sevWarn, "%s 当前不可在线预约（服务端 status）· 选带 ● 的房间", room.Name)
		return m, nil
	}
	m.room.name = room.Name
	m.room.attempt = room.ID
	cmd := m.beginOpenRoom(room.ID)
	if cmd == nil {
		m.pushToast(sevWarn, "该房间正在载入，稍候")
		return m, nil
	}
	// Selecting is selecting: the room's periods load in the panel beside this one,
	// and the keyboard stays here so a failure is reported where it was asked for.
	m.pushToast(sevInfo, "已选中 %s · 正在载入时段", room.Name)
	return m, cmd
}

// selectPlannerDay loads the day under the cursor and chooses its first period. The
// keyboard stays on the planner: loading the day is not a reason to leave it.
func (m *Model) selectPlannerDay() (tea.Model, tea.Cmd) {
	m.repeatEnabled = false
	if m.session.phase != phaseConfirmed {
		m.pushToast(sevWarn, "尚未登录")
		return m, nil
	}
	day := m.selectedDay()
	if day != m.rooms.day {
		m.pushToast(sevInfo, "已选中 %s · 正在载入房间与时段", m.dayOrDash())
		return m, m.adoptDay(day)
	}
	if m.room.id == 0 {
		m.pushToast(sevWarn, "先在 %s 选中一间自习室", paneTitle(PaneRooms))
		return m, nil
	}
	if len(m.room.slots) == 0 {
		m.pushToast(sevWarn, "该日没有可用时段 · 请选择其他日期")
		return m, nil
	}
	// The first period is the one in use, and asking for it now is what makes the
	// seat map meaningful by the time the user gets there.
	m.selectSlot(0)
	m.pushToast(sevOK, "已选中 %s · 正在查询 %s 的座位",
		m.dayOrDash(), m.room.slots[0].StartTime+"–"+m.room.slots[0].EndTime)
	return m, m.querySelectedPeriod()
}

// selectSeat commits the seat under the cursor in the seat list. The decision itself
// belongs to commitSeat, so the list, the map and the preview card all behave the
// same way.
func (m *Model) selectSeat() (tea.Model, tea.Cmd) {
	if m.session.phase != phaseConfirmed {
		m.focusSide(PaneStatus)
		return m.keySession(tea.KeyMsg{Type: tea.KeyEnter})
	}
	if m.room.id == 0 {
		m.pushToast(sevInfo, "先在 %s 选中一间自习室", paneTitle(PaneRooms))
		return m, nil
	}
	number, ok := m.selectedSeatNumber()
	if !ok {
		m.pushToast(sevInfo, "没有可选择的座位")
		return m, nil
	}
	// The keyboard stays where it is: a pre-order is a local memory, not a network
	// write, so the list does not need to hand itself over to confirm it.
	return m.commitSeat(number)
}

// commitSeat is the one 选中/预订 decision for one seat:
//
//   - a seat that can be signed right now is a *selection*: it is signed locally,
//     the preview card opens, and the seat is marked blue with a dot in the list;
//   - anything else -- taken, paused, unqueried, or a day whose window has not
//     opened -- is a *pre-order*, remembered in the project instead of signed.
//
// Every "space on a seat" goes through here, because a key that means one thing in
// the list and another in the map is how a pre-order goes missing.
func (m *Model) commitSeat(number string) (tea.Model, tea.Cmd) {
	if m.db != nil {
		return m.toggleCandidate(number)
	}
	slot, hasSlot := m.room.currentSlot()
	if !hasSlot {
		return m.preorderSelectedSeat()
	}
	if free, _ := m.reservableNow(number); !free {
		return m.preorderSelectedSeat()
	}
	// Selecting is selecting: the preview card fills in beside the map, and the
	// keyboard stays on the seat that was chosen.
	return m, m.beginChooseSeat(number, slot)
}

// seatCommit says how strongly a seat is committed to in the view on screen.
type seatCommit int

const (
	seatUncommitted seatCommit = iota
	// seatPreordered is a seat remembered in the project: a pre-order.
	seatPreordered
	// seatSelected is the seat of the signed selection, which is the one that can
	// actually be submitted.
	seatSelected
)

// chosenSeats is every seat a choice is in force for in the current view: the signed
// selection, plus each seat with a pre-order for this room, day and period. All of
// them are marked, because all of them are recorded and listed in 预订记录.
func (m *Model) chosenSeats() map[string]seatCommit {
	out := map[string]seatCommit{}
	if m.db != nil {
		for _, seat := range m.candidates {
			out[seat] = seatSelected
		}
		return out
	}
	if m.selection != nil && m.selection.SeatNum != "" {
		out[m.selection.SeatNum] = seatSelected
	}
	if len(m.preorders.items) == 0 || m.room.id == 0 {
		return out
	}
	slot, hasSlot := m.room.currentSlot()
	day := m.dayOrDash()
	for _, item := range m.preorders.items {
		if item.RoomID != m.room.id || item.Day != day || item.SeatNum == "" {
			continue
		}
		if _, taken := out[item.SeatNum]; taken {
			continue
		}
		// A record with no period belongs to the whole day; one with a period only
		// marks the period it was made for.
		switch {
		case item.Start == "" && item.End == "":
			out[item.SeatNum] = seatPreordered
		case hasSlot && item.Start == slot.StartTime && item.End == slot.EndTime:
			out[item.SeatNum] = seatPreordered
		}
	}
	return out
}

// chosenSeat is the strongest single commitment in the view, for the places that
// need one answer rather than the whole set: the signed selection when there is one,
// otherwise the newest matching pre-order (the store is kept newest-first).
func (m *Model) chosenSeat() string {
	if m.selection != nil && m.selection.SeatNum != "" {
		return m.selection.SeatNum
	}
	committed := m.chosenSeats()
	for _, item := range m.preorders.items {
		if committed[item.SeatNum] == seatPreordered {
			return item.SeatNum
		}
	}
	return ""
}

// openOverlay takes over the frame with a utility view.
func (m *Model) openOverlay(o Overlay) tea.Cmd {
	if o == m.overlay {
		return nil
	}

	m.zoneBefore = m.zone
	m.overlay = o
	m.overlayFocus = 0
	m.layout()
	if o == OverlayLogs && len(m.logs.entries) == 0 && !m.logs.loading {
		return m.beginLoadLogs()
	}
	return nil
}

// closeOverlay returns to the frame the user was reading, with the keyboard back
// on the panel the view was opened from.
func (m *Model) closeOverlay() {
	if m.overlay.frameOverlay() {
		m.zone = m.zoneBefore
	}
	m.overlay = OverlayNone
	m.overlayFocus = 0
	m.layout()
}

// toggleOverlay opens a utility view, or closes it when it is already open.
func (m *Model) toggleOverlay(o Overlay) tea.Cmd {
	if m.overlay == o {
		m.closeOverlay()
		return nil
	}
	return m.openOverlay(o)
}

// handleBack implements esc. Cancelling the current operation comes before moving
// focus, so esc never silently discards a pending login or an unsaved setting.
func (m *Model) handleBack() (tea.Model, tea.Cmd) {
	// A utility view closes first: it is the innermost thing on screen.
	switch m.overlay {
	case OverlayLogs, OverlayForm:
		m.closeOverlay()
		return m, nil
	}
	if m.addAccountReturn != nil {
		previous := m.addAccountReturn
		m.addAccountReturn = nil
		m.Close()
		previous.width, previous.height, previous.ready = m.width, m.height, m.ready
		previous.focusSide(PaneStatus)
		previous.zone = ZoneSide
		previous.layout()
		previous.pushToast(sevInfo, "已取消新增账号")
		return previous, previous.Init()
	}

	// A QR that is still waiting is the current operation.
	if m.session.phase != phaseConfirmed && (len(m.session.qrGrid) > 0 || m.session.busy) {
		m.closeQRWindow()
		if m.remote != nil {
			m.remote.CancelLoginAsync()
		}
		m.genSession++
		m.session.qrGrid = nil
		m.session.busy = false
		if m.session.phase != phaseConfirmed {
			m.session.phase = phaseIdle
		}
		m.pushToast(sevInfo, "已取消等待扫码")
		return m, nil
	}
	// A running occupancy scan is cancellable.
	if m.room.scanning {
		m.genScan++
		m.room.scanning = false
		m.pushToast(sevInfo, "已停止扫描")
		return m, nil
	}
	// An armed schedule is a pending operation, so esc cancels it first.
	if m.cancelAutoReserve() {
		return m, nil
	}
	// Otherwise esc steps out of the right column, which is the level below.
	if m.overlay.frameOverlay() {
		return m, nil
	}
	if m.zone != ZoneSide {
		// No toast: the focused border already says where the keyboard went, and
		// the hint line is more useful than a message about pressing esc.
		m.zone = ZoneSide
		return m, nil
	}
	// Already in the panel group: esc has nothing to cancel.
	return m, nil
}

// requestQuit asks before discarding a prepared selection. Focus starts on the
// safe choice, so a stray Enter cancels rather than quits.
func (m *Model) requestQuit() (tea.Model, tea.Cmd) {
	if m.selection != nil && !m.quitSkip {
		m.overlay = OverlayConfirm
		m.confirm = confirmState{
			prompt:   "退出并放弃当前选座预览？",
			detail:   "已准备 " + m.selection.SeatNum + " 号座位（未提交）。退出不会发送任何预约请求。",
			confirm:  "退出",
			action:   confirmQuit,
			focusYes: false,
		}
		return m, nil
	}
	m.cancel()
	return m, tea.Quit
}

// serverDay is the day the service says it is. Everything in the day planner is
// relative to it, so the seven rows mean the same thing for every session --
// including after the user has browsed to another one.
func (m *Model) serverDay() (time.Time, bool) {
	base := m.days.anchor
	if base == "" {
		base = m.cfg.Day
	}
	if base == "" {
		base = m.client.Day
	}
	day, err := time.Parse("2006-01-02", base)
	if err != nil {
		return time.Time{}, false
	}
	return day, true
}

// dayAt is the date of a day row, and the label shown for it.
func (m *Model) dayAt(row int) (string, string) {
	day, ok := m.serverDay()
	if !ok {
		return "", "未知日期"
	}
	date := day.AddDate(0, 0, row)
	label := date.Format("01-02") + " " + weekdayNames[int(date.Weekday())]
	switch row {
	case 0:
		label = date.Format("01-02") + " 今天"
	case 1:
		label = date.Format("01-02") + " 明天"
	}
	return date.Format("2006-01-02"), label
}

// selectedDay is the date the period panel's cursor points at. The cron row
// describes a schedule rather than a date, so it keeps the day in use.
func (m *Model) selectedDay() string {
	if m.days.cursor >= dayRows {
		return m.rooms.day
	}
	day, _ := m.dayAt(m.days.cursor)
	return day
}

// moveDayCursor walks the seven days with ] and [.
func (m *Model) moveDayCursor(delta int) (tea.Model, tea.Cmd) {
	m.days.cursor = ((m.days.cursor+delta)%dayRows + dayRows) % dayRows
	return m.applyDayCursor()
}

// resetDayCursor goes back to the server's own day.
func (m *Model) resetDayCursor() (tea.Model, tea.Cmd) {
	m.days.cursor = 0
	return m.applyDayCursor()
}

// adoptDay switches the day being planned. The open room survives it: only what
// belonged to the old day is dropped -- its periods, the queried occupancy, the
// preview signed for it -- and the room is reloaded for the new one. Throwing the
// room away instead is what made a date change look like losing the selection.
func (m *Model) adoptDay(day string) tea.Cmd {
	if m.write.busy {
		m.pushToast(sevWarn, "提交进行中，暂不能切换日期")
		return nil
	}
	m.client.Day = day
	m.rooms.day = day
	m.rooms.all = nil
	m.rooms.filter = ""
	m.rooms.list = listView{}
	m.dropDayScopedState()
	m.layout()

	m.days.autoOpen = m.room.id
	return m.beginListRooms(day)
}

// dropDayScopedState forgets everything that described the old day, keeping the
// room's identity: which room is open does not change when the date does.
func (m *Model) dropDayScopedState() {
	m.candidates = nil
	keepID, keepName := m.room.id, m.room.name
	keepCapacity, keepAttempt := m.room.capacity, m.room.attempt
	layout, hasLayout := m.room.layout, m.room.hasLayout
	seatOrder := m.room.seatOrder
	seatCoords, coordSource := m.room.seatCoords, m.room.coordSource

	if m.selection != nil {
		m.selection = nil
		m.pushToast(sevInfo, "日期已变，已清除选座预览")
	}
	if m.auto.armed {
		m.auto = autoState{}
		m.pushToast(sevInfo, "日期已变，已取消定时预约")
	}

	m.room = roomState{slotIndex: 0}
	m.room.id, m.room.name = keepID, keepName
	m.room.capacity, m.room.attempt = keepCapacity, keepAttempt
	m.room.layout, m.room.hasLayout = layout, hasLayout
	m.room.seatOrder = seatOrder
	m.room.seatCoords, m.room.coordSource = seatCoords, coordSource

	m.rooms.loading = false
	m.genRoom++
	m.genSeats++
	m.genSelect++
	m.genScan++
}

// applyDayCursor loads the day the cursor points at. It is what space on a day
// row does.
func (m *Model) applyDayCursor() (tea.Model, tea.Cmd) {
	if m.session.phase != phaseConfirmed {
		m.pushToast(sevWarn, "尚未登录")
		return m, nil
	}
	if m.days.cursor >= dayRows {
		return m, nil
	}
	m.repeatEnabled = false
	day := m.selectedDay()
	if day == "" {
		m.pushToast(sevWarn, "服务器日期未知，先按 r 刷新房间列表")
		return m, nil
	}
	if day == m.rooms.day {
		// Already loaded: just move the keyboard to what that day describes.
		m.focusZone(ZoneMain)
		return m, nil
	}
	m.pushToast(sevInfo, "%s 的房间与时段…", m.dayOrDash())
	return m, m.adoptDay(day)
}

// resetRoomState forgets everything that belongs to the open room. Anything
// prepared for it -- the selection and an armed schedule -- goes with it, since a
// request built for another room would be wrong.
//
// Abandoning the room also abandons its in-flight requests, so their single-flight
// flags are cleared here. Leaving one set would make every later request a no-op:
// that is a room that can never be opened again, which reads as "space does
// nothing".
func (m *Model) resetRoomState() {
	m.candidates = nil
	if m.selection != nil {
		m.selection = nil
		m.pushToast(sevInfo, "已清除选座预览")
	}
	if m.auto.armed {
		m.auto = autoState{}
		m.pushToast(sevInfo, "已取消定时预约")
	}
	m.room = roomState{slotIndex: 0}
	m.rooms.loading = false
	m.genRoom++
	m.genSeats++
	m.genSelect++
	m.genScan++
	m.layout()
}

// refresh reloads the data the focused panel depends on.
func (m *Model) refresh() (tea.Model, tea.Cmd) {
	switch m.overlay {
	case OverlayLogs:
		return m, m.beginLoadLogs()
	case OverlayForm:
		return m, nil
	}
	// The project panel refreshes by (re)starting the login.
	if m.side == PaneStatus || m.session.phase != phaseConfirmed {
		if m.session.phase == phaseConfirmed {
			return m, m.beginOpenHome()
		}
		return m, m.beginStartQR()
	}
	if m.zone == ZonePreview && m.selection != nil {
		m.pushToast(sevInfo, "选座预览为本地状态；enter 查看完整编码")
		return m, nil
	}
	switch m.side {
	case PaneRecords:
		return m, m.cmdLoadPreorders()
	case PaneRooms:
		return m, m.beginListRooms(m.rooms.day)
	case PanePeriods:
		if m.room.id == 0 {
			m.pushToast(sevWarn, "尚未打开房间")
			return m, nil
		}
		return m, m.beginOpenRoom(m.room.id)
	default:
		if m.room.id == 0 {
			m.pushToast(sevWarn, "尚未打开房间")
			return m, nil
		}
		if slot, ok := m.room.currentSlot(); ok {
			return m, m.beginLoadSeats(slot)
		}
		m.pushToast(sevWarn, "当天没有可用时段")
	}
	return m, nil
}

// --- async entry points ----------------------------------------------------

func (m *Model) beginStartQR() tea.Cmd {
	m.closeQRWindow()
	m.qrWindowDone = false
	m.qrWindowErr = nil
	if m.session.busy {
		return nil
	}
	m.genSession++
	m.session.busy = true
	m.session.phase = phaseStarting
	m.session.err = nil
	m.session.qrGrid = nil
	m.session.callback = nil
	return m.cmdStartQR()
}

func (m *Model) beginOpenHome() tea.Cmd {
	if m.session.busy {
		return nil
	}
	m.genSession++
	m.session.busy = true
	m.session.err = nil
	return m.cmdOpenHome()
}

func (m *Model) beginListRooms(day string) tea.Cmd {
	if m.rooms.loading {
		return nil
	}
	m.genRooms++
	m.rooms.loading = true
	m.rooms.err = nil
	return m.cmdListRooms(day)
}

func (m *Model) beginOpenRoom(roomID int) tea.Cmd {
	if m.write.busy {
		return nil
	}
	if m.room.id != roomID {
		m.candidates = nil
		m.selection = nil
		m.genSelect++
	}
	if m.room.loadingRoom {
		return nil
	}
	m.genRoom++
	m.room.loadingRoom = true
	m.room.err = nil
	m.room.unsupported = nil
	return m.cmdOpenRoom(roomID)
}

func (m *Model) beginLoadSeats(slot chaoxing.Slot) tea.Cmd {
	if m.room.loadingSeats {
		return nil
	}
	m.genSeats++
	m.room.loadingSeats = true
	m.room.err = nil
	return m.cmdSeatMap(slot)
}

// beginChooseSeat prepares the local selection. It needs no in-flight guard,
// because the signature is computed here rather than fetched.
func (m *Model) beginChooseSeat(seat string, slot chaoxing.Slot) tea.Cmd {
	m.genSelect++
	m.room.err = nil
	return m.cmdChooseSeat(seat, slot.StartTime, slot.EndTime)
}

func (m *Model) beginLoadLogs() tea.Cmd {
	if m.logs.loading {
		return nil
	}
	m.genLogs++
	m.logs.loading = true
	m.logs.err = nil
	return m.cmdLoadLogs()
}

// --- async results ---------------------------------------------------------

func (m *Model) handleAsync(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case qrStartedMsg:
		return m.onQRStarted(msg)
	case qrTickMsg:
		if msg.gen == m.genSession && m.session.phase == phaseWaiting {
			return m, m.cmdPollQR()
		}
		return m, nil
	case qrPollMsg:
		return m.onQRPoll(msg)
	case callbackMsg:
		return m.onCallback(msg)
	case homeOpenedMsg:
		return m.onHomeOpened(msg)
	case roomsLoadedMsg:
		return m.onRoomsLoaded(msg)
	case roomOpenedMsg:
		return m.onRoomOpened(msg)
	case seatsLoadedMsg:
		return m.onSeatsLoaded(msg)
	case periodScanMsg:
		return m.onPeriodScan(msg)
	case selectionReadyMsg:
		return m.onSelectionReady(msg)
	case logsLoadedMsg:
		return m.onLogsLoaded(msg)
	case configSavedMsg:
		return m.onConfigSaved(msg)
	case reserveResultMsg:
		return m.onReserveResult(msg)
	case jobsMsg:
		if msg.account != m.cfg.Account {
			return m, nil
		}
		m.jobsLoading = false
		if msg.err != nil {
			m.pushToast(sevErr, "读取预订失败")
		} else {
			m.jobs = msg.items
			m.preorders.list.SetCount(len(m.jobs))
			m.layout()
		}
		return m, nil
	case jobSavedMsg:
		if msg.account != m.cfg.Account {
			return m, nil
		}
		if msg.err != nil {
			m.pushToast(sevErr, "保存失败：%s", msg.err)
		} else {
			m.pushToast(sevOK, "预订记录已更新")
		}
		return m, m.cmdJobs()
	case preordersLoadedMsg:
		return m.onPreordersLoaded(msg)
	case preordersSavedMsg:
		return m.onPreordersSaved(msg)
	}
	return m, nil
}

func (m *Model) onQRStarted(msg qrStartedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.genSession {
		return m, nil
	}
	m.session.busy = false
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return m, nil
		}
		m.session.phase = phaseFailed
		m.session.err = classify(msg.err)
		m.pushToast(sevErr, "二维码获取失败")
		return m, nil
	}
	grid, err := detectQRModules(msg.png)
	if err != nil {
		m.session.phase = phaseFailed
		m.session.err = &AppError{
			Kind:   kindUnknown,
			Short:  "二维码图片无法识别",
			Detail: err.Error(),
			Hint:   "按 r 重新生成二维码；若反复失败请按 d 到诊断视图查看原始响应。",
		}
		return m, nil
	}
	m.session.qrGrid = grid
	m.session.phase = phaseWaiting
	m.session.polls = 0
	m.session.err = nil
	m.session.deadline = time.Now().Add(time.Duration(m.cfg.PollSeconds) * time.Second)
	return m, tea.Batch(m.cmdPollQR(), m.ensureQRWindow())
}

func (m *Model) onQRPoll(msg qrPollMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.genSession {
		return m, nil
	}
	if msg.err != nil {
		m.closeQRWindow()
		if errors.Is(msg.err, context.Canceled) {
			return m, nil
		}
		m.session.phase = phaseFailed
		m.session.err = classify(msg.err)
		return m, nil
	}
	switch msg.state {
	case chaoxing.QRConfirmed:
		m.closeQRWindow()
		// The code is spent once scanned, so it is dropped rather than left on
		// screen while the callback chain runs.
		m.session.qrGrid = nil
		m.session.phase = phaseCompleting
		m.session.busy = true
		m.pushToast(sevOK, "已扫码，正在完成登录")
		return m, m.cmdFollowCallback(msg.target)
	case chaoxing.QRExpired:
		m.closeQRWindow()
		m.session.qrGrid = nil
		m.session.phase = phaseExpired
		m.session.err = nil
		m.pushToast(sevWarn, "二维码已过期，请重新生成")
		return m, nil
	}
	m.session.polls++
	if time.Now().After(m.session.deadline) {
		m.closeQRWindow()
		m.session.qrGrid = nil
		m.session.phase = phaseExpired
		m.session.err = &AppError{
			Kind:  kindSession,
			Short: "等待扫码超时",
			Hint:  "按 enter 重新生成二维码。",
		}
		return m, nil
	}
	return m, m.cmdPollTick(m.genSession)
}

func (m *Model) onCallback(msg callbackMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.genSession {
		return m, nil
	}
	m.session.busy = false
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return m, nil
		}
		m.session.phase = phaseFailed
		m.session.err = classify(msg.err)
		return m, nil
	}
	m.session.callback = msg.result
	// The authoritative check is the seat landing page itself; the AJAX login
	// status alone is not treated as proof of a usable session.
	m.pushToast(sevInfo, "回调完成，正在验证座位页")
	return m, m.beginOpenHome()
}

func (m *Model) onHomeOpened(msg homeOpenedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.genSession {
		return m, nil
	}
	if msg.err == nil && msg.client != nil {
		m.client = msg.client
	}
	m.session.busy = false
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return m, nil
		}
		m.session.phase = phaseFailed
		m.session.err = classify(msg.err)
		m.pushToast(sevErr, "座位页验证失败")
		return m, nil
	}
	if err := m.login.Session.CookieSaveError(); err != nil {
		m.pushToast(sevErr, "登录成功，但会话保存失败")
	}
	m.restoreSession = false
	m.session.home = msg.info
	m.session.phase = phaseConfirmed
	if m.remote != nil {
		if e := m.adoptRemoteIdentity(); e != nil {
			m.session.phase = phaseFailed
			m.session.err = classify(e)
			return m, nil
		}
	}
	if m.db != nil && m.remote == nil {
		if name := m.client.AccountName(); name != "" && name != m.cfg.Account {
			if storage.IsIdentifiedAccount(m.cfg.Account) {
				m.session.phase = phaseFailed
				m.session.err = classify(fmt.Errorf("登录身份与当前账号不一致，请新增账号"))
				return m, nil
			}
			old := m.cfg.Account
			if e := m.db.SetEnabled(false); e != nil {
				m.pushToast(sevErr, "账号迁移失败：%s", e)
				return m, nil
			}
			m.db.Close()
			if e := storage.RenameAccount(m.cfg.Root, old, name); e != nil {
				dir, _ := storage.AccountDir(m.cfg.Root, old)
				m.db, _ = storage.Open(dir)
				m.pushToast(sevErr, "账号迁移失败：%s", e)
				return m, nil
			}
			cfg, _, e := config.LoadAccount(m.cfg.Root, name)
			if e != nil {
				m.pushToast(sevErr, "账号配置加载失败：%s", e)
				return m, nil
			}
			config.Save(cfg)
			dir, _ := storage.AccountDir(m.cfg.Root, name)
			db, e := storage.Open(dir)
			if e == nil {
				db.SetEnabled(cfg.AllowSubmit)
				db.Close()
			}
			return m.switchAccount(name)
		}
	}
	m.session.qrGrid = nil
	m.session.err = nil
	// The planner's seven days hang off the day the service reports.
	m.days.anchor = m.client.Day
	m.rooms.day = m.client.Day
	m.pushToast(sevOK, "已登录，服务器日期 %s", msg.info.ServerDay)

	// The main panel has turned from the login panel into the seat list, and the
	// next useful step is choosing a room.
	m.focusSide(PaneRooms)
	var cmds []tea.Cmd
	if m.remote != nil {
		cmds = append(cmds, m.cmdJobs(), tickCmd(m.cfg.Account))
	}
	if cmd := m.beginListRooms(m.rooms.day); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) onRoomsLoaded(msg roomsLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.genRooms {
		return m, nil
	}
	if msg.err == nil && msg.client != nil {
		m.client = msg.client
	}
	m.rooms.loading = false
	if msg.err != nil {
		m.rooms.err = classify(msg.err)
		return m, nil
	}
	m.rooms.all = msg.rooms
	m.rooms.err = nil
	if msg.day != "" {
		m.rooms.day = msg.day
	}
	m.syncRoomsList()
	m.preselectDefaultRoom()
	m.layout()

	if len(msg.rooms) == 0 {
		m.pushToast(sevWarn, "该日期没有房间")
	} else {
		selectable := 0
		for _, r := range msg.rooms {
			if r.Selectable {
				selectable++
			}
		}
		m.pushToast(sevOK, "载入 %d 个房间，%d 个可选", len(msg.rooms), selectable)
	}
	if id := m.days.autoOpen; id != 0 {
		m.days.autoOpen = 0
		return m, m.beginOpenRoom(id)
	}
	if m.autoPreview() && m.repeatEnabled {
		return m, m.ensureAutomaticIntervals()
	}
	return m, nil
}

func (m *Model) onRoomOpened(msg roomOpenedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.genRoom {
		return m, nil
	}
	if msg.err == nil && msg.client != nil {
		m.client = msg.client
	}
	m.room.loadingRoom = false
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return m, nil
		}
		if chaoxing.IsUnsupported(msg.err) {
			m.room.unsupported = classify(msg.err)
			m.room.err = nil
		} else {
			m.room.err = classify(msg.err)
			m.room.errLabel = "打开失败"
			m.room.unsupported = nil
		}
		// The step failed: the panel that asked says why, and the keyboard stays on
		// it. The seats that are already known stay listed beside it.
		problem := m.room.err
		if problem == nil {
			problem = m.room.unsupported
		}
		m.pushToast(sevErr, "打开失败：%s", problem.Short)
		m.layout()
		return m, nil
	}
	previousID := m.room.id
	m.room.id = msg.roomID
	m.room.name = msg.info.Room
	m.room.capacity = msg.info.Capacity
	// The seat numbering is room metadata. A day whose response carries less of it
	// than the last one must not cost the user the room's seats, so the numbering
	// already known for this same room is kept when the new one is empty.
	layout, ok := m.client.SeatLayout()
	switch {
	case ok && layout.Total > 0:
		m.room.layout, m.room.hasLayout = layout, true
	case previousID == msg.roomID && m.room.hasLayout && m.room.layout.Total > 0:
		// Same room, thinner day: keep the seats.
	default:
		m.room.layout, m.room.hasLayout = layout, false
	}
	// The service's own seat listing, when it sent one, is the room's seat set and
	// its order -- not the numbering range that only list-mode rooms rely on.
	if order := m.client.SeatOrder(); len(order) > 0 {
		m.room.seatOrder = order
	} else if previousID != msg.roomID {
		m.room.seatOrder = nil
	}
	// The service's own positions, when it gave one for every listed seat.
	if coords, source, ok := m.client.SeatCoords(); ok {
		m.room.seatCoords, m.room.coordSource = coords, source
	} else if previousID != msg.roomID {
		m.room.seatCoords, m.room.coordSource = nil, ""
	}
	m.room.endpoints = nil
	m.room.periodExplicit = false
	m.room.slots = msg.info.Slots
	m.room.slotIndex = 0
	if m.autoPreview() {
		m.restoreAutoInterval()
	}
	m.room.filter = ""
	m.room.clearOccupancy()
	m.room.err = nil
	m.room.unsupported = nil
	m.room.periods.SetCount(periodRows)
	// The seat list is the room's numbering, so it is complete the moment the
	// room opens; occupancy is a separate, on-demand query.
	m.room.seats.SetCount(m.staticSeatCount())
	m.room.seats.GotoTop()
	m.layout()

	if len(msg.info.Slots) == 0 {
		// Nothing to work with, so the panel reports why. The seats are still listed,
		// and choosing one is still a pre-order.
		if m.staticSeatCount() > 0 {
			m.pushToast(sevWarn, "%s 没有未来可预约时段 · 仍可在 %s 预订 %d 个座位",
				m.dayOrDash(), paneTitle(PaneSeats), m.staticSeatCount())
		} else {
			m.pushToast(sevWarn, "%s 没有未来可预约时段 · 按 ] 看后一天", m.dayOrDash())
		}
		return m, nil
	}
	m.pushToast(sevOK, "已打开 %s（%d 个时段）· 正在查询 %s 的座位",
		msg.info.Room, len(msg.info.Slots), msg.info.Slots[0].StartTime+"–"+msg.info.Slots[0].EndTime)
	return m, m.beginLoadSeats(msg.info.Slots[0])
}

func (m *Model) onSeatsLoaded(msg seatsLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.genSeats {
		return m, nil
	}
	m.room.loadingSeats = false
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return m, nil
		}
		m.room.err = classify(msg.err)
		m.room.errLabel = "查询失败"
		// The seats are the room's numbering, not the query's answer, so they stay
		// listed and the failure is a toast: no panel is worth taking the keyboard
		// for just to report bad news.
		m.layout()
		m.pushToast(sevErr, "查询失败：%s", m.room.err.Short)
		return m, nil
	}
	m.room.remember(msg.occupancy)
	m.room.err = nil
	m.syncLists()

	// A selection made before the period was known is completed now.
	if pending := m.room.pendingSeat; pending != "" {
		if _, cached := m.room.currentOccupancy(); !cached {
			// The reply described another period than the selection is on, which
			// happens when the cursor moved while the query was in flight: ask for
			// the one the selection belongs to instead of judging it wrongly.
			if cmd := m.querySelectedPeriod(); cmd != nil {
				return m, cmd
			}
			m.room.pendingSeat = ""
			m.pushToast(sevWarn, "未能查询 %s，已取消选定", m.periodOrDash())
			return m, nil
		}
		m.room.pendingSeat = ""
		return m.completePendingSeat(pending)
	}
	if occupancy, ok := m.room.currentOccupancy(); ok {
		// Say how many seats came back: a fully booked period and a failed query
		// are different problems, and the panels are too short to explain either.
		if occupancy.Free == 0 {
			m.pushToast(sevWarn, "%s 没有空闲座位（共 %d 座）· 可换时段",
				m.periodOrDash(), occupancy.Total())
		} else {
			m.pushToast(sevOK, "%s 载入 %d 个空闲座位（共 %d 座）· %s 查看详情",
				m.periodOrDash(), occupancy.Free, occupancy.Total(), paneTitle(PaneSeats))
		}
	}
	return m, nil
}

func (m *Model) onSelectionReady(msg selectionReadyMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.genSelect {
		return m, nil
	}
	if msg.err == nil && msg.client != nil {
		m.client = msg.client
	}
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return m, nil
		}
		m.room.err = classify(msg.err)
		m.room.errLabel = "选座失败"
		m.pushToast(sevErr, "%s", m.room.err.Short)
		return m, nil
	}
	m.selection = msg.sel
	m.layout()
	// The preview card shows itself; the keyboard stays where the choice was made.
	m.pushToast(sevOK, "已准备 %s 号座位预览（未提交）", msg.sel.SeatNum)
	return m, nil
}

func (m *Model) onLogsLoaded(msg logsLoadedMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.genLogs {
		return m, nil
	}
	m.logs.loading = false
	if msg.err != nil {
		m.logs.err = classify(msg.err)
		return m, nil
	}
	m.logs.entries = msg.entries
	m.logs.files = msg.files
	m.logs.err = nil
	m.logs.list.SetCount(len(m.logs.entries))
	m.logs.list.GotoTop()
	m.layout()
	return m, nil
}

func (m *Model) onConfigSaved(msg configSavedMsg) (tea.Model, tea.Cmd) {
	if msg.account != m.cfg.Account {
		return m, nil
	}
	if msg.err != nil {
		m.settings.err = classify(msg.err)
		m.settings.status = ""
		return m, nil
	}
	m.settings.err = nil
	m.settings.dirty = false
	m.settings.status = "已保存到 " + msg.path
	m.cfgPath = msg.path
	if msg.saved.FIDEnc != "" {
		m.cfg = msg.saved
		m.settings.draft = msg.saved
	} else {
		m.cfg = m.settings.draft
	}
	m.cfg.Normalize()
	m.client.SetReserveConfig(reserveConfig(m.cfg))
	m.glyphs = newGlyphs(m.cfg.ASCIIOnly)
	m.spinner = spinner.New(
		spinner.WithSpinner(m.glyphs.Spinner),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(m.theme.Primary)),
	)
	m.pushToast(sevOK, "设置已保存")
	return m, nil
}

// --- shared helpers --------------------------------------------------------

// syncLists hands each panel's interior size to the widget that fills it, so the
// scroll windows match the space the frame will actually give them.
func (m *Model) syncLists() {
	for _, slot := range m.panels {
		if m.overlay.frameOverlay() {
			if slot.owner.zone == ZoneView {
				m.syncViewList(slot)
			}
			continue
		}
		if slot.owner.zone == ZoneMain {
			m.measureGrid(slot)
			continue
		}
		if slot.owner.zone != ZoneSide {
			continue
		}
		switch slot.owner.side {
		case PaneSeats, PanePeriods:
			// The content of these panels is a grid, so the cell count per row is
			// measured here and reused by the movement keys.
			m.measureGrid(slot)
		}
		switch slot.owner.side {
		case PaneRooms:
			m.rooms.list.SetHeight(slot.interiorH)
			m.syncRoomsList()
		case PanePeriods:
			m.room.periods.SetHeight(slot.interiorH)
			m.room.periods.SetCount(periodRows)
		case PaneSeats:
			m.room.seats.SetHeight(slot.interiorH)
			// The list is the room's numbering, not the queried occupancy.
			m.room.seats.SetCount(len(m.visibleSeatNumbers()))
		case PaneRecords:
			m.preorders.list.SetHeight(slot.interiorH)
			if m.db != nil {
				m.preorders.list.SetCount(len(m.jobs))
			} else {
				m.preorders.list.SetCount(len(m.preorders.items))
			}
		}
	}
}

// syncViewList sizes the widgets of a utility view, which has its own panels.
func (m *Model) syncViewList(slot panelSlot) {
	switch m.overlay {
	case OverlayLogs:
		if slot.owner.index == 1 {
			m.logs.detail.Width = slot.interiorW
			m.logs.detail.Height = slot.interiorH
			return
		}
		m.logs.list.SetHeight(slot.interiorH)
		m.logs.list.SetCount(len(m.logs.entries))
	case OverlayForm:
		m.form.Width = slot.interiorW
		m.form.Height = slot.interiorH
		m.syncFormContent()
	}
}

// cmdSeatMap fetches one period's complete seat picture.
func (m *Model) cmdSeatMap(slot chaoxing.Slot) tea.Cmd {
	gen, client, parent := m.genSeats, m.client.Snapshot(), m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, timeoutSeats)
		defer cancel()
		occupancy, err := client.SeatMap(ctx, slot.StartTime, slot.EndTime)
		return seatsLoadedMsg{
			gen: gen, start: slot.StartTime, end: slot.EndTime,
			occupancy: occupancy, err: err,
		}
	}
}

// startFilter opens the inline filter editor for the panels that support it.
func (m *Model) startFilter() (tea.Model, tea.Cmd) {
	if m.overlay.frameOverlay() {
		return m, nil
	}
	var current string
	switch {
	case m.zone == ZoneSide && m.side == PaneRooms:
		current = m.rooms.filter
	case m.zone == ZoneSide && m.side == PaneSeats:
		if m.session.phase != phaseConfirmed || m.room.id == 0 {
			return m, nil
		}
		current = m.room.filter
	default:
		m.pushToast(sevInfo, "该面板不支持过滤")
		return m, nil
	}
	m.filter.active = true
	m.filter.input.SetValue(current)
	m.filter.input.CursorEnd()
	return m, m.filter.input.Focus()
}

// handleFilterKey routes keys to the inline editor while it is open.
func (m *Model) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.filter.active = false
		m.filter.input.Blur()
		m.setFilter("")
		return m, nil
	case tea.KeyEnter:
		m.filter.active = false
		m.filter.input.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.filter.input, cmd = m.filter.input.Update(msg)
	m.setFilter(m.filter.input.Value())
	return m, cmd
}

func (m *Model) setFilter(value string) {
	switch {
	case m.zone == ZoneSide && m.side == PaneRooms:
		m.rooms.filter = value
		m.syncRoomsList()
	case m.zone == ZoneSide && m.side == PaneSeats:
		if m.room.id == 0 {
			return
		}
		m.room.filter = value
		m.room.seats.SetCount(len(m.visibleSeatNumbers()))
	}
}
