package chaoxing

import "strings"

func (c *SeatClient) ValidatePlan(seats []string, start, end string) error {
	if err := c.validatePeriod(start, end); err != nil {
		return err
	}
	if len(seats) < 1 || len(seats) > 3 {
		return inputErr("候选座位必须为 1–3 个")
	}
	known := map[string]bool{}
	for _, seat := range c.grid {
		known[zfill3(seat.SeatNum)] = true
	}
	seen := map[string]bool{}
	for _, seat := range seats {
		if strings.TrimSpace(seat) == "" || !known[zfill3(seat)] || seen[seat] {
			return inputErr("候选座位不属于当前房间或重复")
		}
		seen[seat] = true
	}
	return nil
}

// Snapshot gives an async operation its own mutable room/date state. Immutable
// response data may be shared; containers modified by operations are copied.
func (c *SeatClient) Snapshot() *SeatClient {
	n := *c
	n.rooms = append([]roomListItem(nil), c.rooms...)
	n.grid = append([]gridSeat(nil), c.grid...)
	n.gridKeys = append([]string(nil), c.gridKeys...)
	n.gridCoordKeys = append([]string(nil), c.gridCoordKeys...)
	n.seatMemory = map[int]SeatLayout{}
	for k, v := range c.seatMemory {
		n.seatMemory[k] = v
	}
	if c.Current != nil {
		x := *c.Current
		x.payload = append([]pair(nil), c.Current.payload...)
		n.Current = &x
	}
	return &n
}
