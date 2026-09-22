package tui

import (
	"time"

	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
)

// Every async result carries the generation it was issued for. A reply whose
// generation is no longer current is discarded, so a slow response can never
// overwrite fresher state after the user refreshed or switched rooms.
type (
	qrStartedMsg struct {
		gen int
		png []byte
		err error
	}
	qrPollMsg struct {
		gen    int
		state  chaoxing.QRState
		target string
		err    error
	}
	// qrTickMsg schedules the next single poll; polling is message-driven rather
	// than a blocking loop so the UI stays responsive and cancellation is exact.
	qrTickMsg struct{ gen int }

	callbackMsg struct {
		gen    int
		result map[string]any
		err    error
	}
	homeOpenedMsg struct {
		client *chaoxing.SeatClient
		gen    int
		info   *chaoxing.HomeInfo
		err    error
	}
	roomsLoadedMsg struct {
		client *chaoxing.SeatClient
		gen    int
		day    string
		rooms  []chaoxing.Room
		err    error
	}
	roomOpenedMsg struct {
		client *chaoxing.SeatClient
		gen    int
		roomID int
		info   *chaoxing.RoomOpenInfo
		err    error
	}
	// reserveResultMsg is the service's answer to a reservation or waitlist
	// request. It carries the raw verdict rather than a boolean so the UI can
	// report what actually came back.
	reserveResultMsg struct {
		gen    int
		action confirmAction
		result *chaoxing.ReserveResult
		err    error
	}
	seatsLoadedMsg struct {
		gen       int
		start     string
		end       string
		occupancy *chaoxing.Occupancy
		err       error
	}
	// periodScanMsg is one step of the "check every period" pass.
	periodScanMsg struct {
		gen       int
		index     int
		occupancy *chaoxing.Occupancy
		err       error
	}
	selectionReadyMsg struct {
		client *chaoxing.SeatClient
		gen    int
		sel    *chaoxing.Selection
		err    error
	}
	logsLoadedMsg struct {
		gen     int
		entries []logEntry
		files   []string
		err     error
	}
	configSavedMsg struct {
		saved   config.Config
		account string
		path    string
		err     error
	}

	// tickMsg drives the header clock and toast expiry.
	tickMsg struct {
		at      time.Time
		account string
	}
)

// logEntry is one parsed line of the redacted request log.
type logEntry struct {
	Time      time.Time
	Method    string
	URL       string
	Status    int
	ElapsedMS int64
	Raw       map[string]any
	RawLine   string
}
