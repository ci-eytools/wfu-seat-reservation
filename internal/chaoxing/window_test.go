package chaoxing

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestRoomOpeningIsRoomSpecific(t *testing.T) {
	at := time.Date(2030, 1, 1, 22, 15, 0, 0, Shanghai)
	f := &fakeTransport{handler: func(r *http.Request) *http.Response {
		if r.URL.Path != "/data/apps/seat/room/reserve-window/check" || r.Method != "GET" {
			t.Fatal("unexpected request", r.URL.Path)
		}
		switch r.URL.Query().Get("roomId") {
		case "1":
			return jsonResponse(r, 200, nil, fmt.Sprintf(`{"success":true,"data":{"status":"BEFORE_OPEN","beforeOpenTimeStamp":%d}}`, at.UnixMilli()))
		case "2":
			return jsonResponse(r, 200, nil, `{"success":true,"data":{"status":"AVAILABLE"}}`)
		default:
			return jsonResponse(r, 200, nil, `{"success":true,"data":{"status":"BEFORE_OPEN"}}`)
		}
	}}
	c := NewSeatClient(testLogin(t, f), DefaultFIDEnc, DefaultMappID)
	c.clockAt = at.Add(2 * time.Second)
	c.serverNow = at
	got, e := c.QueryOpening(context.Background(), 1, "2030-01-02")
	if e != nil || !got.Equal(at) {
		t.Fatal(got, e)
	}
	got, e = c.QueryOpening(context.Background(), 2, "2030-01-02")
	if e != nil || !got.IsZero() {
		t.Fatal(got, e)
	}
	if _, e = c.QueryOpening(context.Background(), 3, "2030-01-02"); e == nil {
		t.Fatal("missing timestamp guessed")
	}
}

func TestReserveWindowPreservesServerTimestamp(t *testing.T) {
	at := time.Date(2030, 1, 1, 22, 15, 0, 0, Shanghai)
	c := &SeatClient{windowStatus: "BEFORE_OPEN", windowOpenAt: at, serverNow: at, clockAt: at.Add(1094 * time.Millisecond)}
	_, got, ok := c.ReserveWindow()
	if !ok || !got.Equal(at) {
		t.Fatal("network skew added to opening", got)
	}
}
