package chaoxing

import (
	"context"
	"time"
)

// SeatQuery is independent of another client's navigation state.
type SeatQuery struct {
	Operation string `json:"operation"`
	Day       string `json:"day,omitempty"`
	RoomID    int    `json:"room_id,omitempty"`
	Start     string `json:"start,omitempty"`
	End       string `json:"end,omitempty"`
}
type SeatView struct {
	Order            []string            `json:"order,omitempty"`
	Spots            map[string]SeatSpot `json:"spots,omitempty"`
	GridFields       []string            `json:"grid_fields,omitempty"`
	CoordinateFields []string            `json:"coordinate_fields,omitempty"`
	Home             *HomeInfo           `json:"home,omitempty"`
	Rooms            []Room              `json:"rooms,omitempty"`
	Room             *RoomOpenInfo       `json:"room,omitempty"`
	Occupancy        *Occupancy          `json:"occupancy,omitempty"`
	Grid             []SeatCell          `json:"grid,omitempty"`
	Layout           SeatLayout          `json:"layout"`
	HasLayout        bool                `json:"has_layout"`
	WindowStatus     string              `json:"window_status,omitempty"`
	OpensAt          time.Time           `json:"opens_at,omitempty"`
	ServerTime       time.Time           `json:"server_time"`
	DisplayName      string              `json:"display_name,omitempty"`
}
type SeatRemote interface {
	Query(context.Context, SeatQuery) (*SeatView, error)
}

func (c *SeatClient) remoteQuery(ctx context.Context, op, day string, id int, start, end string) (*SeatView, error) {
	v, e := c.Remote.Query(ctx, SeatQuery{op, day, id, start, end})
	if e != nil {
		return nil, e
	}
	c.remoteView = v
	c.serverNow = v.ServerTime
	c.clockAt = time.Now()
	if day != "" {
		c.Day = day
	}
	if v.Home != nil {
		c.Day = v.Home.ServerDay
	}
	if v.Room != nil {
		c.roomID = id
		c.Day = v.Room.Day
		c.windowStatus = v.WindowStatus
		c.windowOpenAt = v.OpensAt
		c.gridKeys = v.GridFields
		c.gridCoordKeys = v.CoordinateFields
		c.grid = nil
		for _, cell := range v.Grid {
			g := gridSeat{SeatNum: cell.Number}
			if cell.HasCoords {
				col, row := cell.Col, cell.Row
				if cell.Source == "x/y" {
					g.X = &col
					g.Y = &row
				} else {
					g.Col = &col
					g.Row = &row
				}
			}
			c.grid = append(c.grid, g)
		}
	}
	return v, nil
}
func (c *SeatClient) PublicView() *SeatView {
	layout, ok := c.SeatLayout()
	now, _ := c.ServerNow()
	status, at, _ := c.ReserveWindow()
	spots := map[string]SeatSpot{}
	for _, cell := range c.SeatGrid() {
		if spot, ok := c.SeatSpot(cell.Number); ok {
			spots[cell.Number] = spot
		}
	}
	return &SeatView{Order: c.SeatOrder(), Spots: spots, GridFields: c.GridKeys(), CoordinateFields: c.GridCoordKeys(), Grid: c.SeatGrid(), Layout: layout, HasLayout: ok, ServerTime: now, WindowStatus: status, OpensAt: at, DisplayName: c.AccountName()}
}
