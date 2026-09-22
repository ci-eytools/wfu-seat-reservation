package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
)

// Commands perform every side effect. They never touch the model: they run off
// the event loop, carry a generation tag, and always honour a deadline so a
// stalled request cannot hang the UI or leak a goroutine.
//
// Concurrency is bounded by the per-screen in-flight guards in app.go, so at
// most one request is ever in flight against the stateful seat client.

// Per-operation budgets. The login callback chain is the slowest step because it
// walks up to twelve hops.
const (
	timeoutQRStart  = 45 * time.Second
	timeoutQRPoll   = 25 * time.Second
	timeoutCallback = 90 * time.Second
	timeoutHome     = 45 * time.Second
	timeoutRooms    = 60 * time.Second
	timeoutRoomOpen = 60 * time.Second
	timeoutSeats    = 45 * time.Second
	timeoutWrite    = 60 * time.Second
)

// cmdSubmitReservation sends the prepared seat choice. It runs off the event loop
// and reports the service's own verdict; it never retries, because a retried
// write is a second write.
func (m *Model) cmdSubmitReservation() tea.Cmd {
	gen, client, parent := m.genWrite, m.client.Snapshot(), m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, timeoutWrite)
		defer cancel()
		result, err := client.SubmitReservation(ctx)
		return reserveResultMsg{gen: gen, action: confirmReserve, result: result, err: err}
	}
}

func (m *Model) cmdStartQR() tea.Cmd {
	if m.remote != nil {
		gen, client, parent := m.genSession, m.remote, m.ctx
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(parent, timeoutQRStart)
			defer cancel()
			png, e := client.Start(ctx)
			return qrStartedMsg{gen: gen, png: png, err: e}
		}
	}

	gen, login, parent := m.genSession, m.login, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, timeoutQRStart)
		defer cancel()
		png, err := login.Start(ctx)
		return qrStartedMsg{gen: gen, png: png, err: err}
	}
}

// cmdPollQR performs exactly one poll. The next one is scheduled by the update
// loop, which keeps polling cancellable and lets the UI show live progress.
func (m *Model) cmdPollQR() tea.Cmd {
	if m.remote != nil {
		gen, client, parent := m.genSession, m.remote, m.ctx
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(parent, timeoutCallback)
			defer cancel()
			state, e := client.Poll(ctx)
			if e != nil {
				state = chaoxing.QRExpired
			}
			return qrPollMsg{gen: gen, state: state, err: e}
		}
	}

	gen, login, parent := m.genSession, m.login, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, timeoutQRPoll)
		defer cancel()
		state, target, err := login.PollOnce(ctx)
		return qrPollMsg{gen: gen, state: state, target: target, err: err}
	}
}

func (m *Model) cmdPollTick(gen int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return qrTickMsg{gen: gen} })
}

func (m *Model) cmdFollowCallback(target string) tea.Cmd {
	if m.remote != nil {
		gen := m.genSession
		return func() tea.Msg { return callbackMsg{gen: gen, result: map[string]any{"state": "web_login_confirmed"}} }
	}
	gen, login, parent := m.genSession, m.login, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, timeoutCallback)
		defer cancel()
		result, err := login.FollowCallback(ctx, target)
		return callbackMsg{gen: gen, result: result, err: err}
	}
}

func (m *Model) cmdOpenHome() tea.Cmd {
	gen, client, parent := m.genSession, m.client.Snapshot(), m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, timeoutHome)
		defer cancel()
		info, err := client.OpenHome(ctx)
		return homeOpenedMsg{client: client, gen: gen, info: info, err: err}
	}
}

func (m *Model) cmdListRooms(day string) tea.Cmd {
	gen, client, parent := m.genRooms, m.client.Snapshot(), m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, timeoutRooms)
		defer cancel()
		rooms, err := client.ListRooms(ctx, day)
		return roomsLoadedMsg{client: client, gen: gen, day: day, rooms: rooms, err: err}
	}
}

func (m *Model) cmdOpenRoom(roomID int) tea.Cmd {
	gen, client, parent := m.genRoom, m.client.Snapshot(), m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(parent, timeoutRoomOpen)
		defer cancel()
		info, err := client.OpenRoom(ctx, roomID)
		return roomOpenedMsg{client: client, gen: gen, roomID: roomID, info: info, err: err}
	}
}

// cmdChooseSeat prepares the local selection. Every seat is selectable, which is
// what planning ahead needs: the seat's state is reported on screen, and a
// scheduled request is aimed at the moment it may become free. The signature is
// computed here, so this is a pure function of state -- no query, no context.
func (m *Model) cmdChooseSeat(seat, start, end string) tea.Cmd {
	// The selection has its own generation: preparing one is local work, and it
	// must not invalidate a seat query that happens to be in flight.
	gen, client := m.genSelect, m.client.Snapshot()
	return func() tea.Msg {
		sel, err := client.Plan(seat, start, end)
		return selectionReadyMsg{client: client, gen: gen, sel: sel, err: err}
	}
}

func (m *Model) cmdLoadLogs() tea.Cmd {
	gen, dir := m.genLogs, m.cfg.LogDir
	return func() tea.Msg {
		entries, files, err := loadLogEntries(dir)
		return logsLoadedMsg{gen: gen, entries: entries, files: files, err: err}
	}
}

// cmdSaveConfig persists the settings to the user configuration file.
func (m *Model) cmdSaveConfig(cfg config.Config) tea.Cmd {
	if m.remote != nil {
		return m.remoteSave(cfg)
	}
	return func() tea.Msg {
		path, err := config.Save(cfg)
		return configSavedMsg{path: path, err: err, account: cfg.Account, saved: cfg}
	}
}

// tickCmd drives the header clock and toast expiry. It is re-armed by the update
// loop rather than self-scheduling, so exactly one ticker is ever alive.
func tickCmd(accounts ...string) tea.Cmd {
	account := ""
	if len(accounts) > 0 {
		account = accounts[0]
	}
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg{at: t, account: account} })
}
