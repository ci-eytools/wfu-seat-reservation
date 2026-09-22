package chaoxing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// signatureFixtureMD5 is the reference value produced by the original Python
// implementation for the fixture below (hashlib.md5 over the "[k=v]" signing
// text followed by the page's submit_enc token). It pins the port to the
// original signature scheme.
const signatureFixtureMD5 = "cad4de94bc6d45cb500e7fd558d61d99"

// newSelectionFixture mirrors tests/test_selection.py::SelectionTests.setUp.
func newSelectionFixture(t *testing.T) (*SeatClient, *fakeTransport) {
	t.Helper()
	fake := &fakeTransport{handler: func(req *http.Request) *http.Response {
		if strings.HasSuffix(req.URL.Path, "/getusedseatnums") {
			return jsonResponse(req, 200, nil, `{"success":true,"data":{"seatReserves":[{"seatNum":"002"}]}}`)
		}
		return jsonResponse(req, 200, nil, `{"success":true,"data":{}}`)
	}}
	client := NewSeatClient(testLogin(t, fake), DefaultFIDEnc, DefaultMappID)

	client.Day = "2030-01-01"
	client.roomID = 6299
	client.submitEnc = "fixture"
	client.serverNow = time.Date(2030, 1, 1, 8, 0, 0, 0, Shanghai)
	client.clockAt = time.Now()
	client.roomData = &roomInfoData{
		SeatConfig: rawSeatConfig{
			ReserveMode:        intPtr(0),
			TimeType:           intPtr(1),
			MinReserveDuration: 0.5,
			ReserveDuration:    2,
		},
		SeatRoom: rawSeatRoom{FirstLevelName: "Fixture"},
		SeatIntervalMap: map[string][]rawInterval{
			"2": {
				{StartTime: "09:00", EndTime: "09:30"},
				{StartTime: "10:00", EndTime: "10:30"},
			},
		},
		SeatAttributes: []rawSeatAttribute{{SeatNum: "003", IsReserve: intPtr(0)}},
	}
	zero := 0
	for _, n := range []string{"001", "002", "003"} {
		client.grid = append(client.grid, gridSeat{SeatNum: n, ReserveStatus: &zero})
	}
	return client, fake
}

// Ported from test_occupied_disabled_excluded_and_preview_only.
func TestOccupiedDisabledExcludedAndPreviewOnly(t *testing.T) {
	client, _ := newSelectionFixture(t)
	ctx := context.Background()

	available, err := client.AvailableSeats(ctx, "09:00", "09:30")
	if err != nil {
		t.Fatalf("AvailableSeats: %v", err)
	}
	if want := []string{"001"}; !reflect.DeepEqual(available, want) {
		t.Fatalf("available = %v, want %v", available, want)
	}

	selection, err := client.Choose(ctx, "001", "09:00", "09:30")
	if err != nil {
		t.Fatalf("Choose: %v", err)
	}
	summary := selection.Summary()
	if summary.Submitted {
		t.Error("selection must never be submitted")
	}
	if summary.State != "selected_not_submitted" {
		t.Errorf("state = %q", summary.State)
	}
	if !summary.SignaturePrepared {
		t.Error("signature should be prepared")
	}

	form := selection.PreparedForm()
	if !strings.Contains(form, "startTime=09%3A00") {
		t.Errorf("prepared form missing encoded startTime: %s", form)
	}
	if !strings.Contains(form, "enc="+signatureFixtureMD5) {
		t.Errorf("signature differs from the Python implementation: %s", form)
	}

	// The signed payload and the page token must not leak through formatting.
	for _, rendered := range []string{
		fmt.Sprintf("%v", selection),
		fmt.Sprintf("%v", *selection),
		fmt.Sprintf("%#v", selection),
		fmt.Sprintf("%#v", *selection),
	} {
		if strings.Contains(rendered, "fixture") {
			t.Errorf("selection leaked the page token: %s", rendered)
		}
		if strings.Contains(rendered, signatureFixtureMD5) {
			t.Errorf("selection leaked the signature: %s", rendered)
		}
	}

	if err := client.Submit(); !IsBlocked(err) {
		t.Errorf("Submit must always be refused, got %v", err)
	}
}

// Ported from test_gap_rejected_before_query.
func TestGapRejectedBeforeQuery(t *testing.T) {
	client, fake := newSelectionFixture(t)
	if _, err := client.AvailableSeats(context.Background(), "09:00", "10:30"); !IsInput(err) {
		t.Fatalf("want an input error, got %v", err)
	}
	if fake.callCount() != 0 {
		t.Fatalf("a rejected period must not query the network, got %v", fake.calledPaths())
	}
}

// Ported from test_occupied_cannot_be_selected.
func TestOccupiedCannotBeSelected(t *testing.T) {
	client, _ := newSelectionFixture(t)
	if _, err := client.Choose(context.Background(), "002", "09:00", "09:30"); !IsInput(err) {
		t.Fatalf("want an input error, got %v", err)
	}
}

func TestDisabledSeatCannotBeSelected(t *testing.T) {
	client, _ := newSelectionFixture(t)
	if _, err := client.Choose(context.Background(), "003", "09:00", "09:30"); !IsInput(err) {
		t.Fatalf("want an input error, got %v", err)
	}
}

// Unsupported room modes must stop rather than be guessed at.
func TestUnsupportedModesStop(t *testing.T) {
	client, _ := newSelectionFixture(t)

	client.roomData.SeatConfig.ReserveMode = intPtr(1)
	if _, err := client.TimeSlots(); !IsUnsupported(err) {
		t.Errorf("reserveMode=1: want unsupported, got %v", err)
	}

	client.roomData.SeatConfig.ReserveMode = intPtr(0)
	client.roomData.SeatConfig.TimeType = intPtr(0)
	if _, err := client.TimeSlots(); !IsUnsupported(err) {
		t.Errorf("timeType=0: want unsupported, got %v", err)
	}
}

// A missing reserveMode is not the normal mode, matching Python's None != 0.
func TestMissingReserveModeIsUnsupported(t *testing.T) {
	client, _ := newSelectionFixture(t)
	client.roomData.SeatConfig.ReserveMode = nil
	if _, err := client.TimeSlots(); !IsUnsupported(err) {
		t.Fatalf("want unsupported, got %v", err)
	}
}

func TestPausedSlotsAreExcluded(t *testing.T) {
	client, _ := newSelectionFixture(t)
	// Pause the whole day; every slot must disappear.
	client.roomData.SeatRoom.SeatPauseDate = json.RawMessage(
		`{"dateJson":"[{\"startDate\":\"2030-01-01 00:00:00\",\"endDate\":\"2030-01-01 23:59:00\"}]"}`)

	slots, err := client.TimeSlots()
	if err != nil {
		t.Fatalf("TimeSlots: %v", err)
	}
	if len(slots) != 0 {
		t.Fatalf("paused slots should be excluded, got %v", slots)
	}
}

func TestPastSlotsAreExcluded(t *testing.T) {
	client, _ := newSelectionFixture(t)
	// The server clock already passed 09:00, so only the 10:00 slot remains.
	client.serverNow = time.Date(2030, 1, 1, 9, 45, 0, 0, Shanghai)
	client.clockAt = time.Now()

	slots, err := client.TimeSlots()
	if err != nil {
		t.Fatalf("TimeSlots: %v", err)
	}
	if len(slots) != 1 || slots[0].StartTime != "10:00" {
		t.Fatalf("slots = %v, want only the 10:00 slot", slots)
	}
}

func TestDurationLimitsEnforced(t *testing.T) {
	client, _ := newSelectionFixture(t)
	// 09:00-10:00 is not a contiguous run of configured slots.
	if _, err := client.AvailableSeats(context.Background(), "09:00", "10:00"); !IsInput(err) {
		t.Errorf("contiguous run: want input error, got %v", err)
	}
	// Reverse order must be rejected.
	if _, err := client.AvailableSeats(context.Background(), "09:30", "09:00"); !IsInput(err) {
		t.Errorf("reversed period: want input error, got %v", err)
	}
}

// Ported from the work/probe scripts' understanding of the seat list payload:
// list mode expands the configured seat range.
func TestSeatNumberPadding(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"1", "001"},
		{1, "001"},
		{float64(24), "024"},
		{"024", "024"},
		{"0024", "0024"},
		{"196", "196"},
	}
	for _, tc := range cases {
		if got := zfill3(tc.in); got != tc.want {
			t.Errorf("zfill3(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseSubmitEncIsOrderIndependent(t *testing.T) {
	cases := []struct {
		name string
		html string
		want string
	}{
		{"id first", `<input type="hidden" id="submit_enc" value="abc123">`, "abc123"},
		{"value first", `<input value="abc123" id="submit_enc">`, "abc123"},
		{"upper case and entity", `<INPUT ID="submit_enc" VALUE='abc&amp;123'>`, "abc&123"},
		{"unquoted", `<input id=submit_enc value=abc123>`, "abc123"},
		{"other input", `<input id="other" value="x">`, ""},
		{"no value", `<input id="submit_enc">`, ""},
		{"no input", `<div>nothing</div>`, ""},
	}
	for _, tc := range cases {
		if got := parseSubmitEnc(tc.html); got != tc.want {
			t.Errorf("%s: parseSubmitEnc = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRoomNameJoinsNonEmptyLevels(t *testing.T) {
	room := roomListItem{FirstLevelName: "主校区", SecondLevelName: "一楼", ThirdLevelName: "101自修室"}
	if got, want := RoomName(room), "主校区 - 一楼 - 101自修室"; got != want {
		t.Errorf("RoomName = %q, want %q", got, want)
	}
	gaps := roomListItem{FirstLevelName: "主校区", ThirdLevelName: "101自修室"}
	if got, want := RoomName(gaps), "主校区 - 101自修室"; got != want {
		t.Errorf("RoomName with a gap = %q, want %q", got, want)
	}
}

// The room list query must be issued with the original parameters.
func TestListRoomsIssuesExpectedQuery(t *testing.T) {
	fake := &fakeTransport{handler: func(req *http.Request) *http.Response {
		return jsonResponse(req, 200, nil,
			`{"success":true,"data":{"totalPage":1,"seatRoomList":[{"id":6299,"firstLevelName":"主校区","secondLevelName":"一楼","thirdLevelName":"101自修室","capacity":120,"status":0,"isShow":0}]}}`)
	}}
	client := NewSeatClient(testLogin(t, fake), DefaultFIDEnc, DefaultMappID)

	rooms, err := client.ListRooms(context.Background(), "2030-01-01")
	if err != nil {
		t.Fatalf("ListRooms: %v", err)
	}
	if len(rooms) != 1 {
		t.Fatalf("rooms = %v", rooms)
	}
	if !rooms[0].Selectable {
		t.Error("room with status=0,isShow=0 should be selectable")
	}
	if rooms[0].Name != "主校区 - 一楼 - 101自修室" {
		t.Errorf("name = %q", rooms[0].Name)
	}

	req := fake.requests[0]
	query := req.URL.Query()
	for key, want := range map[string]string{
		"cpage": "1", "pageSize": "100", "day": "2030-01-01", "deptIdEnc": DefaultFIDEnc,
		"firstLevelName": "", "secondLevelName": "", "thirdLevelName": "", "time": "",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}
}

func TestListRoomsRejectsBadDay(t *testing.T) {
	client := NewSeatClient(testLogin(t, &fakeTransport{}), DefaultFIDEnc, DefaultMappID)
	if _, err := client.ListRooms(context.Background(), "16/09/2026"); !IsInput(err) {
		t.Fatalf("want input error, got %v", err)
	}
}

// SeatMap must keep the taken seats so the UI can mark them, and must agree with
// AvailableSeats about which seats are free.
func TestSeatMapMarksOccupiedAndDisabled(t *testing.T) {
	client, fake := newSelectionFixture(t)
	// 001 free, 002 occupied, 003 disabled by seatAttributes.
	zero := 0
	client.grid = append(client.grid, gridSeat{SeatNum: "004", ReserveStatus: intPtr(1)})

	occupancy, err := client.SeatMap(context.Background(), "09:00", "09:30")
	if err != nil {
		t.Fatalf("SeatMap: %v", err)
	}
	if len(occupancy.Seats) != 4 {
		t.Fatalf("SeatMap returned %d seats, want 4", len(occupancy.Seats))
	}
	want := map[string]SeatStatus{
		"001": SeatFree,
		"002": SeatOccupied,
		"003": SeatDisabled,
		"004": SeatUnavailable,
	}
	for _, seat := range occupancy.Seats {
		if got := want[seat.Num]; seat.Status != got {
			t.Errorf("seat %s status = %v, want %v", seat.Num, seat.Status, got)
		}
	}
	if occupancy.Free != 1 || occupancy.Occupied != 1 || occupancy.Disabled != 1 || occupancy.Unavailable != 1 {
		t.Fatalf("counts = free:%d occupied:%d disabled:%d unavailable:%d",
			occupancy.Free, occupancy.Occupied, occupancy.Disabled, occupancy.Unavailable)
	}
	if occupancy.Total() != 4 {
		t.Errorf("total = %d, want 4", occupancy.Total())
	}

	// The free subset must match AvailableSeats exactly.
	available, err := client.AvailableSeats(context.Background(), "09:00", "09:30")
	if err != nil {
		t.Fatalf("AvailableSeats: %v", err)
	}
	free := []string{}
	for _, seat := range occupancy.Seats {
		if seat.Status == SeatFree {
			free = append(free, seat.Num)
		}
	}
	if !reflect.DeepEqual(available, free) {
		t.Fatalf("AvailableSeats = %v but SeatMap free = %v", available, free)
	}
	if len(fake.calledPaths()) != 2 {
		t.Errorf("each call should issue exactly one query, got %v", fake.calledPaths())
	}
	_ = zero
}

func TestSeatStatusStrings(t *testing.T) {
	for status, want := range map[SeatStatus]string{
		SeatFree: "空闲", SeatOccupied: "已占用", SeatDisabled: "不可选", SeatUnavailable: "不可用",
	} {
		if got := status.String(); got != want {
			t.Errorf("SeatStatus(%d) = %q, want %q", status, got, want)
		}
	}
}

// A day whose room/info omits the seat range must not cost the room its seats.
// The numbering is room metadata: what the room reported before is reused, and the
// day's own room list is the next resort.
func TestSeatLayoutFallsBackToTheRoomsKnownRange(t *testing.T) {
	client := NewSeatClient(testLogin(t, &fakeTransport{}), DefaultFIDEnc, DefaultMappID)
	client.Day = "2030-01-02"
	client.roomID = 6299
	client.seatMemory = map[int]SeatLayout{6299: {Start: 21, Total: 40, Mode: "列表模式"}}
	// The day's answer carries neither capacity nor startSeatNum.
	client.roomData = &roomInfoData{SeatRoom: rawSeatRoom{PicSeatMode: intPtr(1)}}

	layout, ok := client.SeatLayout()
	if !ok || layout.Start != 21 || layout.Total != 40 {
		t.Fatalf("layout = %+v ok=%v, want the room's remembered 21..60", layout, ok)
	}
	if got := len(client.SeatGrid()); got != 40 {
		t.Fatalf("the grid was not rebuilt from the remembered range: %d seats", got)
	}
	if number, ok := client.SeatNumber(39); !ok || number != "060" {
		t.Fatalf("SeatNumber(39) = %q ok=%v, want 060", number, ok)
	}

	// Nothing remembered: the day's own room list still says how many seats there
	// are, so the room is not blank.
	fresh := NewSeatClient(testLogin(t, &fakeTransport{}), DefaultFIDEnc, DefaultMappID)
	fresh.Day = "2030-01-02"
	fresh.roomID = 6299
	fresh.rooms = []roomListItem{{ID: 6299, Capacity: 12}}
	fresh.roomData = &roomInfoData{SeatRoom: rawSeatRoom{}}
	layout, ok = fresh.SeatLayout()
	if !ok || layout.Total != 12 || layout.Start != 1 {
		t.Fatalf("layout = %+v ok=%v, want 1..12 from the room list", layout, ok)
	}

	// A different room never borrows another room's numbering.
	other := NewSeatClient(testLogin(t, &fakeTransport{}), DefaultFIDEnc, DefaultMappID)
	other.Day = "2030-01-02"
	other.roomID = 7000
	other.seatMemory = map[int]SeatLayout{6299: {Start: 21, Total: 40}}
	other.roomData = &roomInfoData{SeatRoom: rawSeatRoom{PicSeatMode: intPtr(1)}}
	if layout, _ := other.SeatLayout(); layout.Total != 0 {
		t.Fatalf("a room without a range invented one: %+v", layout)
	}
}

// The field names come from the response, not from a schema the tool hoped for.
func TestGridListingFieldsAreObservedNotAssumed(t *testing.T) {
	client, _ := newSelectionFixture(t)

	// A listing with positions.
	client.observeGridKeys(json.RawMessage(`{"seatDatas":[
		{"seatNum":"001","reserveStatus":0,"x":1,"y":2},
		{"seatNum":"002","reserveStatus":1,"row":3,"col":4}]}`))
	if got := strings.Join(client.GridKeys(), ","); got != "col,reserveStatus,row,seatNum,x,y" {
		t.Fatalf("fields = %q, want the union of the listing's own keys", got)
	}
	if got := strings.Join(client.GridCoordKeys(), ","); got != "col,row,x,y" {
		t.Fatalf("coordinate fields = %q, want the ones the listing carried", got)
	}

	// A listing without any: the answer is "the service sent no positions".
	client.observeGridKeys(json.RawMessage(`{"seatDatas":[{"seatNum":"001","reserveStatus":0}]}`))
	if got := strings.Join(client.GridKeys(), ","); got != "reserveStatus,seatNum" {
		t.Fatalf("fields = %q, want just the two", got)
	}
	if got := client.GridCoordKeys(); len(got) != 0 {
		t.Fatalf("coordinate fields = %v, want none", got)
	}

	// An empty or unparsable listing claims nothing.
	client.observeGridKeys(json.RawMessage(`{"seatDatas":[]}`))
	if got := client.GridKeys(); len(got) != 0 {
		t.Fatalf("fields = %v, want none", got)
	}
	client.gridKeys = []string{"stale"}
	client.observeGridKeys(json.RawMessage(`not json`))
	if got := client.GridKeys(); len(got) != 0 {
		t.Fatalf("fields = %v, want none after an unreadable listing", got)
	}
}

func TestContinuousSlotsUseServerUnitAndPause(t *testing.T) {
	c, _ := newSelectionFixture(t)
	c.roomData.SeatConfig.TimeType = intPtr(0)
	c.roomData.SeatConfig.TimeUnit = intPtr(30)
	c.roomData.SeatRoom.StartTime = "2030-01-01 09:00:00"
	c.roomData.SeatRoom.EndTime = "2030-01-01 21:00:00"
	slots, err := c.TimeSlots()
	if err != nil || len(slots) != 24 || slots[0].EndTime != "09:30" || slots[23].EndTime != "21:00" {
		t.Fatal(slots, err)
	}
	c.roomData.CommonPauseTimes = [][]string{{"2030-01-01 10:00:00", "2030-01-01 10:30:00"}}
	slots, err = c.TimeSlots()
	if err != nil || len(slots) != 23 {
		t.Fatal(slots, err)
	}
	if c.validatePeriod("09:30", "11:00") == nil {
		t.Fatal("pause crossed")
	}
	c.roomData.CommonPauseTimes = nil
	c.roomData.SeatConfig.TimeUnit = intPtr(0)
	slots, err = c.TimeSlots()
	if err != nil || len(slots) != 1 {
		t.Fatal(slots, err)
	}
}

func TestRoomEpochTimesDoNotBreakFixedSlots(t *testing.T) {
	c, _ := newSelectionFixture(t)
	// Observed room/info represents these clocks as Java timestamps, including zero.
	if err := json.Unmarshal([]byte(`{"startTime":0,"endTime":50400000}`), &c.roomData.SeatRoom); err != nil {
		t.Fatal(err)
	}
	slots, err := c.TimeSlots()
	if err != nil || len(slots) != 2 {
		t.Fatal(slots, err)
	}
	c.roomData.SeatConfig.TimeType = intPtr(0)
	c.roomData.SeatConfig.TimeUnit = intPtr(30)
	c.serverNow = time.Date(2029, 12, 31, 23, 0, 0, 0, Shanghai)
	slots, err = c.TimeSlots()
	if err != nil || len(slots) != 28 || slots[0].StartTime != "08:00" || slots[27].EndTime != "22:00" {
		t.Fatal(slots, err)
	}
}
