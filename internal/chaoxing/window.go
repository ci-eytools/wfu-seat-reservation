package chaoxing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// QueryOpening reads the selected room/date window without loading or submitting a seat.
func (c *SeatClient) QueryOpening(ctx context.Context, roomID int, day string) (time.Time, error) {
	if roomID <= 0 {
		return time.Time{}, inputErr("房间无效")
	}
	if _, e := time.Parse("2006-01-02", day); e != nil {
		return time.Time{}, e
	}
	raw, e := c.jsonCall(ctx, http.MethodGet, "/data/apps/seat/room/reserve-window/check", url.Values{"roomId": {strconv.Itoa(roomID)}, "day": {day}, "deptIdEnc": {c.FIDEnc}, "fidEnc": {c.FIDEnc}})
	if e != nil {
		return time.Time{}, e
	}
	var w rawReserveWindow
	if e = json.Unmarshal(raw, &w); e != nil {
		return time.Time{}, e
	}
	switch strings.ToUpper(w.Status) {
	case "AVAILABLE":
		return time.Time{}, nil
	case "BEFORE_OPEN":
		if w.BeforeOpenTimeStamp > 0 {
			at := time.UnixMilli(w.BeforeOpenTimeStamp).In(Shanghai)
			if c.Login.Session.Log != nil {
				c.Login.Session.Log.Event("reservation_window", map[string]any{"room_id": roomID, "day": day, "status": w.Status, "server_open_ms": w.BeforeOpenTimeStamp, "server_open_time": at.Format(time.RFC3339Nano)})
			}
			return at, nil
		}
	}
	return time.Time{}, inputErr("服务端未提供可用开放时间：" + w.Status)
}
