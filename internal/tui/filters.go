package tui

import (
	"strconv"
	"strings"

	"wfuseat/internal/chaoxing"
)

// filterRooms narrows the room list. A filter never changes the underlying data,
// so clearing it restores the full list without another request.
func filterRooms(rooms []chaoxing.Room, filter string) []chaoxing.Room {
	needle := strings.ToLower(strings.TrimSpace(filter))
	if needle == "" {
		return rooms
	}
	out := make([]chaoxing.Room, 0, len(rooms))
	for _, room := range rooms {
		if strings.Contains(strings.ToLower(room.Name), needle) ||
			strings.Contains(strconv.Itoa(room.ID), needle) {
			out = append(out, room)
		}
	}
	return out
}

// filterSeatNumbers narrows the static seat list. The list shows numbers only, so
// the filter matches numbers only.
func filterSeatNumbers(numbers []string, filter string) []string {
	needle := strings.TrimSpace(filter)
	if needle == "" {
		return numbers
	}
	out := make([]string, 0, len(numbers))
	for _, number := range numbers {
		if strings.Contains(number, needle) {
			out = append(out, number)
		}
	}
	return out
}

// filterSeatStates matches a seat number or a status word, so "/空闲" lists the
// free seats and "02" lists the seats numbered 02x.
func filterSeatStates(seats []chaoxing.SeatState, filter string) []chaoxing.SeatState {
	needle := strings.TrimSpace(filter)
	if needle == "" {
		return seats
	}
	out := make([]chaoxing.SeatState, 0, len(seats))
	for _, seat := range seats {
		if strings.Contains(seat.Num, needle) || strings.Contains(seat.Status.String(), needle) {
			out = append(out, seat)
		}
	}
	return out
}
