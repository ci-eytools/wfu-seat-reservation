package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"

	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
)

const (
	toastTTL  = 6 * time.Second
	maxToasts = 3
)

// toast is a transient notification rendered in place of the hint line.
type toast struct {
	text    string
	sev     severity
	expires time.Time
}

type sessionPhase int

const (
	phaseIdle sessionPhase = iota
	phaseStarting
	phaseWaiting
	// phaseCompleting covers the OAuth callback and the seat-page verification
	// that follows it; the confirmation is not treated as success on its own.
	phaseCompleting
	phaseConfirmed
	phaseExpired
	phaseFailed
)

type sessionState struct {
	phase sessionPhase

	// qrGrid holds the decoded module grid. The payload it encodes is a live
	// login credential: it is never logged, displayed, or persisted.
	qrGrid   [][]bool
	deadline time.Time
	polls    int
	busy     bool

	home     *chaoxing.HomeInfo
	callback map[string]any
	err      *AppError
}

// The period panel is a day planner: seven days, each of which the room list and
// the period grid describe.
const (
	dayRows    = 7
	periodRows = dayRows + 1
)

// dayState is the period panel's own state.
type dayState struct {
	// cursor selects one of the seven days, or the repeat row.
	cursor int
	// applied is the day the loaded room list belongs to; empty means the
	// server's own day.
	applied string
	// anchor is the service's own day. The seven rows are relative to it, so
	// browsing tomorrow must not redefine what "today" means.
	anchor string
	// autoOpen is a room to reopen once a day switch has reloaded the list, so
	// choosing a day lands on that day's periods instead of an empty panel.
	autoOpen int
}

type roomsState struct {
	day  string
	all  []chaoxing.Room
	list listView

	filter  string
	loading bool
	err     *AppError
	gen     int
}

// visibleRooms applies the current filter.
func (r *roomsState) visibleRooms() []chaoxing.Room {
	return filterRooms(r.all, r.filter)
}

type roomState struct {
	id       int
	name     string
	capacity int

	// layout is the room's seat numbering and arrangement, which is what the
	// static seat list shows. It needs no query: it arrives with the room.
	layout    chaoxing.SeatLayout
	hasLayout bool

	// seatOrder is the room's seats in the order the service listed them, kept only
	// when the service actually sent a listing (grid rooms). It is then the seat
	// set and its order; empty means the numbering itself is the order.
	seatOrder []string

	// seatCoords is the position the service gave for each listed seat (x, y), and
	// coordSource names the fields it came from. They are room metadata like the
	// numbering, so they survive a date change with it.
	seatCoords  map[string][2]int
	coordSource string

	// attempt is the room the last open tried. A failure is reported against it,
	// so the error is visible on the panel that asked even though no room opened.
	attempt int

	slots          []chaoxing.Slot
	slotIndex      int
	endpoints      []int
	periodExplicit bool
	periods        listView
	seats          listView

	// occupancy caches one period's complete seat picture, keyed by "start-end".
	// Switching back and forth therefore costs nothing, and the seat's state in
	// other periods can be reported without another request.
	occupancy map[string]*chaoxing.Occupancy

	// filter narrows the seat list for the selected period only.
	filter string

	// pendingSeat is a seat the user selected before its period had been queried.
	// The query lands, and the selection is made then, so one key press is enough.
	pendingSeat string

	// scanning drives the explicit "check every period" pass.
	scanning bool
	scanDone int

	loadingRoom  bool
	loadingSeats bool

	err *AppError
	// errLabel names the step that failed -- opening, querying, scanning -- so the
	// panels do not call a query failure an open failure.
	errLabel    string
	unsupported *AppError

	gen int
}

// currentSlot returns the selected period, if any.
func (r *roomState) currentSlot() (chaoxing.Slot, bool) {
	if r.periodExplicit && len(r.endpoints) == 0 {
		return chaoxing.Slot{}, false
	}
	if r.slotIndex < 0 || r.slotIndex >= len(r.slots) {
		return chaoxing.Slot{}, false
	}
	if len(r.endpoints) > 0 {
		lo, hi := r.endpoints[0], r.endpoints[0]
		for _, i := range r.endpoints {
			if i < lo {
				lo = i
			}
			if i > hi {
				hi = i
			}
		}
		if lo < 0 || hi >= len(r.slots) {
			return chaoxing.Slot{}, false
		}
		for i := lo + 1; i <= hi; i++ {
			if r.slots[i-1].EndTime != r.slots[i].StartTime {
				return chaoxing.Slot{}, false
			}
		}
		return chaoxing.Slot{StartTime: r.slots[lo].StartTime, EndTime: r.slots[hi].EndTime}, true
	}
	return r.slots[r.slotIndex], true
}

func occupancyKey(start, end string) string { return start + "-" + end }

func (r *roomState) occupancyFor(start, end string) (*chaoxing.Occupancy, bool) {
	value, ok := r.occupancy[occupancyKey(start, end)]
	return value, ok
}

// currentOccupancy is the seat picture for the selected period, if it has been
// fetched.
func (r *roomState) currentOccupancy() (*chaoxing.Occupancy, bool) {
	slot, ok := r.currentSlot()
	if !ok {
		return nil, false
	}
	return r.occupancyFor(slot.StartTime, slot.EndTime)
}

// remember caches a period's seat picture.
func (r *roomState) remember(occupancy *chaoxing.Occupancy) {
	if occupancy == nil {
		return
	}
	if r.occupancy == nil {
		r.occupancy = map[string]*chaoxing.Occupancy{}
	}
	r.occupancy[occupancyKey(occupancy.Start, occupancy.End)] = occupancy
}

// clearOccupancy drops cached seat data, which every room change must do.
func (r *roomState) clearOccupancy() {
	r.occupancy = nil
	r.scanning = false
	r.scanDone = 0
}

// writeState is the outcome of the last reservation attempt. It is kept so the
// details card can keep showing what the service said, instead of losing it when
// a toast expires.
type writeState struct {
	what   string
	result *chaoxing.ReserveResult
	err    *AppError
	busy   bool
}

// preordersState is the 预订记录 panel: the intents remembered in the project's
// own store.
type preordersState struct {
	items []preorderRecord
	list  listView
	path  string
	err   *AppError
}

type logsState struct {
	entries []logEntry
	files   []string

	list   listView
	detail viewport.Model

	loading bool
	err     *AppError
	gen     int
}

type settingsState struct {
	// draft is the edited copy; it only replaces the live config once saved.
	draft   config.Config
	list    listView
	editing bool
	input   textinput.Model
	dirty   bool
	err     *AppError
	status  string
}

// textInput builds the settings field editor.
func textInput() textinput.Model {
	input := textinput.New()
	input.Prompt = "> "
	input.CharLimit = 200
	input.Width = 48
	return input
}

// filterState is the shared inline filter editor. Only one list is filtered at a
// time, so a single editor is enough and key routing stays unambiguous.
type filterState struct {
	active bool
	input  textinput.Model
}

// confirmAction names the work a confirmation performs. Actions are identified
// by value rather than by a captured closure, so the confirm handler always runs
// against the current model instead of a stale copy.
type confirmAction int

const (
	confirmNone confirmAction = iota
	confirmQuit
	confirmReserve
	confirmAutoReserve
	confirmCancelPreorder
	confirmDeleteAccount
	confirmSchedule
	confirmCancelJob
)

// confirmState is the safe-by-default confirmation overlay. Focus always starts
// on the safe choice so a stray Enter cannot confirm.
type confirmState struct {
	prompt   string
	detail   string
	request  string
	confirm  string
	action   confirmAction
	focusYes bool
}
