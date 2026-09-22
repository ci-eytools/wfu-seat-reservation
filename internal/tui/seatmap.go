package tui

import (
	"sort"
	"strings"
)

// The coordinate plan
//
// When the service hands over a position for every seat it lists, the map is drawn
// from those positions instead of flowing the seats by number: the distinct x values
// become the columns, the distinct y values the rows, in the service's own order. A
// coordinate with no seat stays empty, which is what an aisle looks like, and a plan
// taller or wider than the panel is scrolled rather than squeezed.
//
// A position must be there for *every* listed seat, and all of them must come from
// the same pair of fields. A map built from half the positions would be a guess, and
// then the plain number flow -- which claims nothing about geometry -- is used.

// seatPos is a seat's place on the plan: row 0 at the top, column 0 at the left.
type seatPos struct{ row, col int }

// seatPlan is the arrangement of the seats currently on screen.
type seatPlan struct {
	bySeat map[string]seatPos
	byPos  map[seatPos]string
	rows   int
	cols   int
	// source names the fields the positions came from: "x/y" or "row/col".
	source string
	// compressed is true when the coordinate values were too far apart to use as
	// they are (pixel-like values) and were squeezed to their order instead.
	compressed bool
}

// buildSeatPlan arranges numbers by their coordinates. It returns nil unless every
// one of them has a position.
func buildSeatPlan(numbers []string, coords map[string][2]int, source string) *seatPlan {
	if len(numbers) == 0 || len(coords) < len(numbers) {
		return nil
	}
	xs := make(map[int]bool, len(numbers))
	ys := make(map[int]bool, len(numbers))
	for _, number := range numbers {
		xy, ok := coords[number]
		if !ok {
			return nil
		}
		xs[xy[0]] = true
		ys[xy[1]] = true
	}
	colOf, cols, xCompressed := axisIndex(xs)
	rowOf, rows, yCompressed := axisIndex(ys)
	plan := &seatPlan{
		bySeat:     make(map[string]seatPos, len(numbers)),
		byPos:      make(map[seatPos]string, len(numbers)),
		rows:       rows,
		cols:       cols,
		source:     source,
		compressed: xCompressed || yCompressed,
	}
	for _, number := range numbers {
		xy := coords[number]
		pos := seatPos{row: rowOf[xy[1]], col: colOf[xy[0]]}
		plan.bySeat[number] = pos
		if _, taken := plan.byPos[pos]; !taken {
			plan.byPos[pos] = number
		}
	}
	return plan
}

// axisIndex maps each distinct coordinate on one axis to its grid index, and reports
// how many cells the axis has.
//
// The coordinate values are used as they are when they sit close together, so a gap
// the service left -- an aisle between x=1 and x=3 -- stays a visible empty column.
// A coordinate system whose values are far apart (pixels, or a scale in the hundreds)
// is squeezed to the values' order instead, because a map a thousand columns wide is
// not a map; the plan says it was compressed.
func axisIndex(values map[int]bool) (index map[int]int, size int, compressed bool) {
	sorted := make([]int, 0, len(values))
	for value := range values {
		sorted = append(sorted, value)
	}
	sort.Ints(sorted)
	if len(sorted) == 0 {
		return map[int]int{}, 0, false
	}
	span := sorted[len(sorted)-1] - sorted[0] + 1
	index = make(map[int]int, len(sorted))
	if span <= 4*len(sorted)+8 {
		for _, value := range sorted {
			index[value] = value - sorted[0]
		}
		return index, span, false
	}
	for rank, value := range sorted {
		index[value] = rank
	}
	return index, len(sorted), true
}

// seatPlan is the plan for the seats on screen, or nil when the service reported no
// usable positions and the map should flow the seats by number.
func (m *Model) seatPlan() *seatPlan {
	if len(m.room.seatCoords) == 0 {
		return nil
	}
	return buildSeatPlan(m.visibleSeatNumbers(), m.room.seatCoords, m.room.coordSource)
}

// moveSeatPlan moves the cursor to the neighbouring seat: h/l along the seat's own
// row, j/k to the nearest seat in the next row that has one. Empty cells are
// skipped, because an aisle is not a seat.
func (m *Model) moveSeatPlan(plan *seatPlan, dRow, dCol int) {
	number, ok := m.selectedSeatNumber()
	if !ok {
		return
	}
	pos, ok := plan.bySeat[number]
	if !ok {
		return
	}
	switch {
	case dCol != 0:
		for col := pos.col + dCol; col >= 0 && col < plan.cols; col += dCol {
			if other, found := plan.byPos[seatPos{row: pos.row, col: col}]; found {
				m.focusSeatNumber(other)
				return
			}
		}
	case dRow != 0:
		for row := pos.row + dRow; row >= 0 && row < plan.rows; row += dRow {
			if other, found := plan.nearestInRow(row, pos.col); found {
				m.focusSeatNumber(other)
				return
			}
		}
	}
}

// nearestInRow is the seat in one row whose column is closest to want.
func (p *seatPlan) nearestInRow(row, want int) (string, bool) {
	best, bestDistance := "", 0
	for col := 0; col < p.cols; col++ {
		number, found := p.byPos[seatPos{row: row, col: col}]
		if !found {
			continue
		}
		distance := col - want
		if distance < 0 {
			distance = -distance
		}
		if best == "" || distance < bestDistance {
			best, bestDistance = number, distance
		}
	}
	return best, best != ""
}

// firstSeat and lastSeat are the ends of the plan in reading order.
func (p *seatPlan) firstSeat() string {
	for row := 0; row < p.rows; row++ {
		for col := 0; col < p.cols; col++ {
			if number, found := p.byPos[seatPos{row: row, col: col}]; found {
				return number
			}
		}
	}
	return ""
}

func (p *seatPlan) lastSeat() string {
	for row := p.rows - 1; row >= 0; row-- {
		for col := p.cols - 1; col >= 0; col-- {
			if number, found := p.byPos[seatPos{row: row, col: col}]; found {
				return number
			}
		}
	}
	return ""
}

// focusSeatNumber puts the cursor on one seat of the visible list.
func (m *Model) focusSeatNumber(number string) {
	for index, candidate := range m.visibleSeatNumbers() {
		if candidate == number {
			m.seatCursorSet(index)
			return
		}
	}
}

// seatPlanRows draws the plan, windowed on the cursor in both directions so the
// selection is never scrolled out of view. A plan taller than the panel scrolls
// vertically with j/k, and one wider than it scrolls horizontally with h/l.
func (m *Model) seatPlanRows(plan *seatPlan, width, height int, focused bool) []string {
	number, _ := m.selectedSeatNumber()
	cursor, placed := plan.bySeat[number]
	if !placed {
		cursor = seatPos{}
	}
	visibleCols := max(width/seatCellWidth, 1)
	firstRow, lastRow := windowAround(cursor.row, plan.rows, height)
	firstCol, lastCol := windowAround(cursor.col, plan.cols, visibleCols)

	committed := m.chosenSeats()
	lines := make([]string, 0, height)
	for row := firstRow; row < lastRow; row++ {
		parts := make([]string, 0, visibleCols)
		for col := firstCol; col < lastCol; col++ {
			seat, found := plan.byPos[seatPos{row: row, col: col}]
			if !found {
				// A coordinate with no seat: empty space, kept so the aisles show.
				parts = append(parts, strings.Repeat(" ", seatCellWidth))
				continue
			}
			parts = append(parts, m.seatCell(seat, seat == number, committed[seat], focused))
		}
		lines = append(lines, m.fit(strings.Join(parts, ""), width))
	}
	return lines
}

// windowAround is the slice of size long, ending at most at total, that keeps want
// inside and roughly centred.
func windowAround(want, total, size int) (int, int) {
	if size <= 0 || total <= 0 {
		return 0, 0
	}
	if total <= size {
		return 0, total
	}
	first := clampInt(want-size/2, 0, total-size)
	return first, first + size
}

// seatPlanSize describes the plan on screen, so a scrolled or compressed map says so
// instead of silently hiding or moving seats.
func (m *Model) seatPlanSize(plan *seatPlan) string {
	if plan == nil {
		return ""
	}
	size := plan.source + " 排布 " + itoa(plan.cols) + "×" + itoa(plan.rows)
	if plan.compressed {
		size += "（坐标间距过大，已按顺序压缩）"
	}
	return size
}
