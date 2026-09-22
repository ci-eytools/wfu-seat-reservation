package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/skip2/go-qrcode"

	"wfuseat/internal/chaoxing"
	"wfuseat/internal/config"
)

// seatDay is the date the fake service reports; it must be a Wednesday for the
// interval-map key below to be the one that the client reads.
const seatDay = "2026-09-16"

// apiFake serves the real endpoints with the real response shapes. Only the
// network is fake: the guard, the HTML parsing, the QR decoding, the signature
// and the whole state machine are the production code paths.
type apiFake struct {
	mu      sync.Mutex
	paths   []string
	methods []string
	bodies  []string

	qrPNG []byte
	// qrWaiting keeps the scan status pending, so the polling deadline can be
	// exercised.
	qrWaiting bool
	// badSeatPage makes the landing page fail the read-only evidence check, which
	// is how the client detects a session that is not usable.
	badSeatPage bool
	// noForm serves a seat page without a submit form, so the write path has to
	// refuse rather than invent a target.
	noForm bool
	// pastToday makes the server's own day have no periods left, which is what an
	// evening session sees: the seat page still loads and names its form, but
	// there is nothing left to book today.
	pastToday bool
	// manyPeriods serves a day with a dozen periods, so the grid is exercised with
	// more cells than fit on one row.
	manyPeriods bool
	// windowClosed makes the reservation window refuse, which is how a room open
	// fails after the seat page has already been usable.
	windowClosed bool
	// noSubmitToken serves the select page without its submit form, which is what
	// a day whose reservation window has not opened yet looks like.
	noSubmitToken bool
	// selectPageStatus, when set, answers the select page with that status code:
	// the real service redirects or errors instead of serving the page before the
	// window opens.
	selectPageStatus int
	// thinInfoDay is a day whose room/info omits the seat range: an unopened day's
	// response can carry less than an open one's, and the seats must survive it.
	thinInfoDay string
	// failInfoDay is a day whose room/info refuses outright.
	failInfoDay string
	// failSeatsDay is a day whose seat query refuses: the room and its periods
	// load, but nothing can be said about the seats.
	failSeatsDay string
	// gridRoom serves a picSeatMode=2 room, the only mode whose seat listing the
	// service itself sends.
	gridRoom bool
}

func (f *apiFake) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.paths = append(f.paths, req.URL.Path)
	f.methods = append(f.methods, req.Method)
	f.mu.Unlock()
	body := ""
	if req.Body != nil {
		if raw, err := io.ReadAll(req.Body); err == nil {
			body = string(raw)
			// Restore the body: the handler needs it, and so does anything that
			// inspects the request form the way the real server would.
			req.Body = io.NopCloser(strings.NewReader(body))
		}
	}
	f.mu.Lock()
	f.bodies = append(f.bodies, body)
	f.mu.Unlock()
	resp := f.respond(req, body)
	resp.Request = req
	return resp, nil
}

func (f *apiFake) requestedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

// bodyFor returns the recorded body of the first request to a path.
func (f *apiFake) bodyFor(path string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.paths {
		if p == path && i < len(f.bodies) {
			return f.bodies[i]
		}
	}
	return ""
}

// writesTo counts the requests sent to one path.
func (f *apiFake) writesTo(path string) int {
	count := 0
	for _, p := range f.requestedPaths() {
		if p == path {
			count++
		}
	}
	return count
}

func (f *apiFake) respond(req *http.Request, body string) *http.Response {
	host, path := req.URL.Hostname(), req.URL.Path
	// Form parameters arrive in the body for POSTs, exactly as the real service
	// takes them, so the form is what the fixture has to read.
	form, _ := url.ParseQuery(body)

	switch {
	case host == "e.wfu.edu.cn" && path == "/ssoApi/appQRCode":
		page := fmt.Sprintf("var UUID = \"7285eeaa-8d55-424e-9776-1ef1d9449c59\";\nvar baseImg = \"data:image/png;base64,%s\";\n",
			base64.StdEncoding.EncodeToString(f.qrPNG))
		return textResponse(req, 200, page)

	case host == "e.wfu.edu.cn" && path == "/ssoApi/checkQRLogin":
		if f.qrWaiting {
			return textResponse(req, 200, `{"code":"0x0000000000"}`)
		}
		return textResponse(req, 200, `{"code":"0x000000","data":{"locatUrl":"https://tyrzfw.chaoxing.com/OAuth2/wfu/index"}}`)

	case host == "tyrzfw.chaoxing.com" && path == "/OAuth2/wfu/index":
		return textResponse(req, 200, callbackPage)

	case host == "tyrzfw.chaoxing.com" && path == "/OAuth2/wfu/login":
		return textResponse(req, 200, `{"status":true}`)

	case host == "office.chaoxing.com" && path == "/front/third/apps/seat/index":
		if f.badSeatPage {
			return textResponse(req, 200, `<html><head><title>请登录</title></head><body>请先登录</body></html>`)
		}
		return textResponse(req, 200, seatIndexPage)

	case host == "office.chaoxing.com" && path == "/front/third/apps/seat/select":
		if f.selectPageStatus != 0 {
			resp := textResponse(req, f.selectPageStatus, "redirected")
			if f.selectPageStatus/100 == 3 {
				resp.Header.Set("Location", "/front/third/apps/seat/list")
			}
			return resp
		}
		return textResponse(req, 200, f.seatPage())

	case path == reserveTestPath:
		// The fixture's own reservation target, named by the page above. The
		// "test-only" name keeps it obviously synthetic.
		return textResponse(req, 200, `{"status":true,"msg":"预约成功（测试）"}`)

	case path == "/data/apps/seat/index":
		// The seat page's own index call, which carries the live reservations.
		return textResponse(req, 200, `{"success":true,"data":{"curReserves":[
			{"id":777,"seatNum":"005","roomId":6299,"firstLevelName":"主校区","secondLevelName":"二楼",
			 "thirdLevelName":"107自修室","startTime":1789000000000,"endTime":1789001800000,
			 "duration":0.5,"status":0}],"seatConfig":{}}}`)

	case path == "/data/apps/seat/cancel":
		return textResponse(req, 200, `{"status":true,"msg":"已取消预约（测试）"}`)

	case path == "/data/apps/seat/room/list":
		return textResponse(req, 200, `{"success":true,"data":{"totalPage":1,"seatRoomList":[
			{"id":6299,"firstLevelName":"主校区","secondLevelName":"一楼","thirdLevelName":"101自修室","capacity":3,"status":0,"isShow":0},
			{"id":6300,"firstLevelName":"主校区","secondLevelName":"二楼","thirdLevelName":"201自修室","capacity":50,"status":1,"isShow":0}]}}`)

	case path == "/data/apps/seat/room/reserve-window/check":
		if f.windowClosed {
			return textResponse(req, 200, `{"success":true,"data":{"status":"AFTER_CLOSE"}}`)
		}
		if f.pastToday && req.URL.Query().Get("day") != seatDay {
			// The next day's window is not open yet, and the page says when it
			// opens, which is what a scheduled request aims at.
			openAt := time.Now().Add(time.Hour).UnixMilli()
			return textResponse(req, 200, fmt.Sprintf(
				`{"success":true,"data":{"status":"BEFORE_OPEN","beforeOpenTimeStamp":%d}}`, openAt))
		}
		return textResponse(req, 200, `{"success":true,"data":{"status":"AVAILABLE"}}`)

	case path == "/data/apps/seat/room/info":
		serverNow := time.Date(2026, 9, 16, 18, 0, 0, 0, chaoxing.Shanghai).UnixMilli()
		day := firstNonEmpty(form.Get("toDay"), req.URL.Query().Get("toDay"))
		if f.failInfoDay != "" && day == f.failInfoDay {
			return textResponse(req, 200, `{"success":false,"msg":"该日期尚未开放预约（测试）"}`)
		}
		every := `{"startTime":"19:00","endTime":"19:30"}`
		intervals := fmt.Sprintf(`{"1":[%s],"2":[%s],"3":[%s],"4":[%s],"5":[%s],"6":[%s],"7":[%s]}`,
			every, every, every, every, every, every, every)
		if f.thinInfoDay != "" && day == f.thinInfoDay {
			// No capacity, no startSeatNum: the numbering is simply not repeated,
			// which is what a day whose window has not opened looks like.
			return textResponse(req, 200, fmt.Sprintf(`{"success":true,"data":{
				"seatRoom":{"firstLevelName":"主校区","secondLevelName":"一楼","thirdLevelName":"101自修室",
					"status":0,"isShow":0,"isOpen":0,"roleShow":true,"picSeatMode":1},
				"seatConfig":{"reserveMode":0,"timeType":1,"minReserveDuration":0.5,"reserveDuration":2},
				"seatIntervalMap":%s,
				"serverNow":%d}}`, intervals, serverNow))
		}
		if f.gridRoom {
			// A grid room: the service sends its own seat listing, which is the only
			// place a seat's position could come from.
			return textResponse(req, 200, fmt.Sprintf(`{"success":true,"data":{
				"seatRoom":{"firstLevelName":"主校区","secondLevelName":"一楼","thirdLevelName":"101自修室",
					"capacity":4,"status":0,"isShow":0,"isOpen":0,"roleShow":true,"picSeatMode":2,"startSeatNum":1},
				"seatConfig":{"reserveMode":0,"timeType":1,"minReserveDuration":0.5,"reserveDuration":2},
				"seatIntervalMap":%s,
				"seatAttributes":[{"seatNum":"002","isReserve":0}],
				"serverNow":%d}}`, intervals, serverNow))
		}
		if f.manyPeriods {
			slots := ""
			for hour := 19; hour < 24; hour++ {
				if hour > 19 {
					slots += ","
				}
				slots += fmt.Sprintf(`{"startTime":"%02d:00","endTime":"%02d:30"}`, hour, hour)
			}
			return textResponse(req, 200, fmt.Sprintf(`{"success":true,"data":{
				"seatRoom":{"firstLevelName":"主校区","secondLevelName":"二楼","thirdLevelName":"107自修室",
					"capacity":420,"status":0,"isShow":0,"isOpen":0,"roleShow":true,"picSeatMode":1,"startSeatNum":1},
				"seatConfig":{"reserveMode":0,"timeType":1,"minReserveDuration":0.5,"reserveDuration":2},
				"seatIntervalMap":{"1":[%s],"2":[%s],"3":[%s],"4":[%s],"5":[%s],"6":[%s],"7":[%s]},
				"seatAttributes":[{"seatNum":"003","isReserve":0}],
				"serverNow":%d}}`, slots, slots, slots, slots, slots, slots, slots, serverNow))
		}
		if f.pastToday && firstNonEmpty(form.Get("toDay"), req.URL.Query().Get("toDay")) == seatDay {
			// No periods left on the server's own day.
			return textResponse(req, 200, fmt.Sprintf(`{"success":true,"data":{
				"seatRoom":{"firstLevelName":"主校区","secondLevelName":"一楼","thirdLevelName":"101自修室",
					"capacity":3,"status":0,"isShow":0,"isOpen":0,"roleShow":true,"picSeatMode":1,"startSeatNum":1},
				"seatConfig":{"reserveMode":0,"timeType":1,"minReserveDuration":0.5,"reserveDuration":2},
				"seatIntervalMap":{},
				"seatAttributes":[{"seatNum":"003","isReserve":0}],
				"serverNow":%d}}`, serverNow))
		}
		return textResponse(req, 200, fmt.Sprintf(`{"success":true,"data":{
			"seatRoom":{"firstLevelName":"主校区","secondLevelName":"一楼","thirdLevelName":"101自修室",
				"capacity":3,"status":0,"isShow":0,"isOpen":0,"roleShow":true,"picSeatMode":1,"startSeatNum":1},
			"seatConfig":{"reserveMode":0,"timeType":1,"minReserveDuration":0.5,"reserveDuration":2},
			"seatIntervalMap":{"1":[{"startTime":"19:00","endTime":"19:30"}],"2":[{"startTime":"19:00","endTime":"19:30"}],
				"3":[{"startTime":"19:00","endTime":"19:30"},{"startTime":"19:30","endTime":"20:00"}],
				"4":[{"startTime":"19:00","endTime":"19:30"}],"5":[{"startTime":"19:00","endTime":"19:30"}],
				"6":[{"startTime":"19:00","endTime":"19:30"}],"7":[{"startTime":"19:00","endTime":"19:30"}]},
			"seatAttributes":[{"seatNum":"003","isReserve":0}],
			"serverNow":%d}}`, serverNow))

	case path == "/data/apps/seat/seatgrid/roomid":
		// The service's own listing for a grid room. x/y are included on purpose:
		// the tool must report them, not silently invent a layout from them.
		// Deliberately not in number order: the service's order is the room's, and
		// the seat list has to follow it. The x values skip 2 on purpose: that gap
		// is an aisle, and the map has to keep it.
		return textResponse(req, 200, `{"success":true,"data":{"seatDatas":[
			{"seatNum":"003","reserveStatus":0,"x":1,"y":2},
			{"seatNum":"001","reserveStatus":0,"x":1,"y":1},
			{"seatNum":"004","reserveStatus":0,"x":3,"y":2},
			{"seatNum":"002","reserveStatus":1,"x":3,"y":1}]}}`)

	case path == "/data/apps/seat/getusedseatnums":
		if f.failSeatsDay != "" && form.Get("day") == f.failSeatsDay {
			return textResponse(req, 200, `{"success":false,"msg":"该日期座位暂不可查询（测试）"}`)
		}
		return textResponse(req, 200, `{"success":true,"data":{"seatReserves":[{"seatNum":"002"}]}}`)
	}
	return textResponse(req, 404, "not found")
}

// The fixture seat page declares where its own form goes, which is the only way
// the client learns a write target. The paths are deliberately named "test-only"
// so nobody mistakes this fixture for a recorded endpoint: the repository has no
// record of the real one.
const (
	reserveTestPath = "/data/apps/seat/room/reserve-test-only"
)

// seatPage is the select page. noForm drops the form, which is how a page that
// submits from JavaScript looks.
func (f *apiFake) seatPage() string {
	if f.noForm || f.noSubmitToken {
		// No form and no token: the page is readable, but nothing can be signed.
		return `<html><body><p>预约窗口未开放</p></body></html>`
	}
	return `<html><body>
	<form id="seatForm" action="` + reserveTestPath + `" method="post">
		<input type="hidden" id="submit_enc" value="submit-token-value">
	</form>
	</body></html>`
}

// callbackPage reproduces the real callback template, including the escapes the
// original lookaround-based regex handled.
const callbackPage = `<!DOCTYPE html><html><head><script type="text/javascript">
    var url = "\/OAuth2\/wfu\/login";
    jQuery.ajax({
        url: url,
        async: false,
        data: {
            data: "Zm9vYmFyLXRva2Vu",
            time: 1789555039354,
            enc: "AAAA+BBB/CCC=",
            displayName: "\u6602\u667A\u4F1F",
            userRole: 3
        },
        dataType: "json",
        success: function (data) { if (data.status) { res = 1; } }
    });
</script></head></html>`

// seatIndexPage carries exactly the three pieces of evidence the read-only page
// check requires, plus the server clock the client synchronises to.
const seatIndexPage = `<html><head><title>座位预约</title>
<script type="text/javascript" src="/static/apps/seat/index.js"></script>
<script>var userLoginInfo = { "userInfo": { "name": "test" } };
serverNow = new Date('2026-09-16T18:00:00.000+08:00'),
</script></head><body>座位预约</body></html>`

func textResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        http.Header{"Content-Type": {"text/html;charset=utf-8"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

// makeQRPNG renders a real QR image, mirroring the server's borderless render.
func makeQRPNG(t *testing.T, content string) []byte {
	t.Helper()
	qr, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		t.Fatalf("qrcode.New: %v", err)
	}
	qr.DisableBorder = true
	modules := len(qr.Bitmap())
	// A fresh instance: go-qrcode's encode() is not idempotent.
	img, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		t.Fatalf("qrcode.New: %v", err)
	}
	img.DisableBorder = true
	raw, err := img.PNG(4 * modules)
	if err != nil {
		t.Fatalf("PNG: %v", err)
	}
	return raw
}

// testQRPNG is the QR image the fake login endpoint serves.
func testQRPNG(t *testing.T) []byte {
	t.Helper()
	return makeQRPNG(t, "https://e.wfu.edu.cn/ssoApi/checkQRUUID?uuid=7285eeaa-8d55-424e-9776-1ef1d9449c59")
}

// validQRModuleCount reports whether n is a legal QR symbol size.
func validQRModuleCount(n int) bool {
	return n >= 21 && n <= 177 && (n-17)%4 == 0
}

// newFlowModel wires the model to the fake service.
func newFlowModel(t *testing.T) (*Model, *apiFake) {
	t.Helper()
	fake := &apiFake{qrPNG: testQRPNG(t)}
	return newModelWithTransport(t, fake), fake
}

// newModelWithTransport builds a model whose session talks to the given
// transport instead of the network.
func newModelWithTransport(t *testing.T, transport http.RoundTripper) *Model {
	t.Helper()
	logDir := t.TempDir()
	session, err := chaoxing.NewSessionWithConfig(chaoxing.SessionConfig{
		BaseTransport: transport,
		LogDir:        logDir,
	})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	session.UserAgent = chaoxing.BrowserUserAgent
	login := &chaoxing.QRLogin{Session: session, Tokens: map[string]string{}}

	cfg := config.Default()
	cfg.Proxy = ""
	cfg.LogDir = logDir
	m, err := New(cfg, "/tmp/wfuseat-flow-config.json", WithSession(login))
	if m != nil {
		m.qrWindowLauncher = func(context.Context, [][]bool) error { return context.Canceled }
	}
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(m.Close)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return m
}

// plainLines joins rendered panel lines with their colour escapes stripped, so an
// assertion is about what a person reads rather than about the styling.
func plainLines(lines []string) string {
	for i, line := range lines {
		lines[i] = ansi.Strip(line)
	}
	return strings.Join(lines, "\n")
}

func keyMsg(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

// feed delivers one message and returns the command it produced.
func feed(t *testing.T, m *Model, msg tea.Msg) tea.Cmd {
	t.Helper()
	_, cmd := m.Update(msg)
	return cmd
}

// exec runs a command and collects the messages it produced, without feeding
// them. Batches are unwrapped. Poll ticks are never executed, so the test never
// sleeps and stays in full control of pacing.
func exec(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	var out []tea.Msg
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			for _, sub := range msg {
				walk(sub)
			}
		default:
			out = append(out, msg)
		}
	}
	walk(cmd)
	return out
}

// feedAll delivers every message and batches the commands they produced.
func feedAll(t *testing.T, m *Model, msgs []tea.Msg) tea.Cmd {
	t.Helper()
	var cmds []tea.Cmd
	for _, msg := range msgs {
		if cmd := feed(t, m, msg); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// drain feeds a command's messages and runs whatever they schedule until the
// chain goes quiet. Poll ticks are never executed, so the test never sleeps.
func drain(t *testing.T, m *Model, cmd tea.Cmd, budget int) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && steps < budget; steps++ {
		next := queue[0]
		queue = queue[1:]
		if next == nil {
			continue
		}
		msgs := exec(t, next)
		if len(msgs) == 0 {
			continue
		}
		if follow := feedAll(t, m, msgs); follow != nil {
			queue = append(queue, follow)
		}
	}
}

// TestFullReadOnlyWorkflow is the end-to-end check: scan, log in, verify the seat
// page, list rooms, open a room, inspect a period's seats, prepare a selection --
// while never sending a request that could change anything.
func TestFullReadOnlyWorkflow(t *testing.T) {
	m, fake := newFlowModel(t)

	// Stage 1: request a QR code.
	startMsgs := exec(t, feed(t, m, keyMsg('r')))
	if len(startMsgs) != 1 {
		t.Fatalf("starting the login produced %d messages, want 1", len(startMsgs))
	}
	pollCmd := feedAll(t, m, startMsgs)

	// The PNG must be turned back into modules, and the payload -- a live login
	// credential -- must never be rendered as text.
	if n := len(m.session.qrGrid); !validQRModuleCount(n) {
		t.Fatalf("recovered %d modules from the PNG (%v), which is not a valid QR size", n, m.session.err)
	}
	if m.session.phase != phaseWaiting {
		t.Fatalf("phase after starting = %v, want waiting", m.session.phase)
	}
	view := m.View()
	if !strings.Contains(view, "剩余") {
		t.Errorf("QR view is missing the countdown hint:\n%s", view)
	}
	if strings.Contains(view, "checkQRUUID") || strings.Contains(view, "7285eeaa") {
		t.Error("the QR payload (a live credential) must never be rendered as text")
	}

	// Stage 2: confirm, run the callback chain, verify the seat landing page.
	drain(t, m, pollCmd, 40)
	if m.session.phase != phaseConfirmed {
		t.Fatalf("phase after verification = %v (err %v), want confirmed", m.session.phase, m.session.err)
	}
	if m.session.home == nil || m.session.home.ServerDay != seatDay {
		t.Fatalf("seat home = %+v, want server day %s", m.session.home, seatDay)
	}
	if got := m.login.BlockedSeatRequests(); got != 0 {
		t.Errorf("the normal flow must not trip the read-only guard (%d)", got)
	}

	// Stage 3: the room list loaded automatically after login.
	if len(m.rooms.all) != 2 {
		t.Fatalf("rooms = %d, want 2", len(m.rooms.all))
	}
	if m.zone != ZoneSide || m.side != PaneRooms {
		t.Fatalf("focus = %v, want the room panel after login", describeFocus(m))
	}
	if !m.rooms.all[0].Selectable || m.rooms.all[1].Selectable {
		t.Error("selectable flags do not match the served status values")
	}

	// Stage 4: space commits the first room. Selecting does not move the keyboard,
	// but the room's periods load beside it all the same.
	drain(t, m, chainStep(t, m, PaneRooms), 40)
	if m.room.id != 6299 {
		t.Fatalf("opened room = %d, want 6299", m.room.id)
	}
	if m.side != PaneRooms || m.zone != ZoneSide {
		t.Fatalf("focus = %v, want to stay on the room panel", describeFocus(m))
	}
	// The period panel filled in without being focused.
	if len(m.room.slots) != 2 {
		t.Fatalf("slots = %v, want the two configured periods", m.room.slots)
	}
	if len(m.room.slots) != 2 {
		t.Fatalf("slots = %v, want the two configured periods", m.room.slots)
	}

	// Stage 5: the first period's seat map: 001 free, 002 occupied, 003 disabled.
	occupancy, ok := m.room.currentOccupancy()
	if !ok {
		t.Fatalf("the first period was not fetched (err %v)", m.room.err)
	}
	if occupancy.Start != "19:00" {
		t.Fatalf("loaded period = %s, want 19:00", occupancy.Start)
	}
	want := map[string]chaoxing.SeatStatus{
		"001": chaoxing.SeatFree,
		"002": chaoxing.SeatOccupied,
		"003": chaoxing.SeatDisabled,
	}
	for _, seat := range occupancy.Seats {
		if seat.Status != want[seat.Num] {
			t.Errorf("seat %s status = %v, want %v", seat.Num, seat.Status, want[seat.Num])
		}
	}
	if occupancy.Free != 1 || occupancy.Occupied != 1 || occupancy.Disabled != 1 {
		t.Fatalf("counts = free:%d occupied:%d disabled:%d", occupancy.Free, occupancy.Occupied, occupancy.Disabled)
	}
	// The chain has moved to the day panel, so the content is that day's period
	// grid, and the auto-queried period reports its counts in the cell.
	seatView := m.View()
	for _, text := range []string{"19:00–19:30", "空闲"} {
		if !strings.Contains(seatView, text) {
			t.Errorf("the period grid does not carry %q:\n%s", text, seatView)
		}
	}

	// Stage 6: the seat panel reports what the seat is doing, and the preview card
	// prepares the reservation for it.
	feed(t, m, keyMsg('3'))
	if m.side != PaneSeats || m.zone != ZoneSide {
		t.Fatalf("3 did not focus the seat panel (%v)", describeFocus(m))
	}
	feed(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.zone != ZoneMain {
		t.Fatalf("enter on the seat panel left the keyboard at %v", describeFocus(m))
	}
	report := m.View()
	for _, text := range []string{"位置", "该时段"} {
		if !strings.Contains(report, text) {
			t.Errorf("the seat map does not carry %q:\n%s", text, report)
		}
	}
	feed(t, m, keyMsg(previewKey()))
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeyEnter}), 40)
	if m.selection == nil {
		t.Fatalf("no selection was prepared (err %v)", m.room.err)
	}
	if m.zone != ZonePreview {
		t.Fatalf("focus = %v, want the preview card after preparing a seat", describeFocus(m))
	}
	summary := m.selection.Summary()
	if summary.SeatNum != "001" || summary.Submitted || !summary.SignaturePrepared {
		t.Fatalf("summary = %+v", summary)
	}
	if !strings.Contains(m.selection.PreparedForm(), "enc=") {
		t.Error("the prepared form should carry the signature")
	}

	// Stage 7: esc returns to the panel group, and moving the period cursor then
	// querying the period it landed on fetches the next one.
	feed(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.zone != ZoneSide {
		t.Fatalf("esc from the preview card gave %v, want the panel group", describeFocus(m))
	}
	feed(t, m, keyMsg('2'))
	if m.side != PanePeriods || m.zone != ZoneSide {
		t.Fatalf("2 focused %v, want the day planner", describeFocus(m))
	}
	// The day's periods live in the content grid, so the period is chosen there.
	feed(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.zone != ZoneMain {
		t.Fatalf("enter on the day planner gave %v, want the period grid", describeFocus(m))
	}
	// h/l move within a row of the grid, which is the neighbouring period, and
	// space is what fetches it.
	feed(t, m, tea.KeyMsg{Type: tea.KeyRight})
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.room.slotIndex != 1 {
		t.Fatalf("slot index = %d, want 1", m.room.slotIndex)
	}
	if occupancy, ok := m.room.currentOccupancy(); !ok || occupancy.Start != "19:30" {
		t.Fatalf("the second period was not fetched (occupancy %+v)", occupancy)
	}

	// Stage 8: the read-only invariant.
	for _, path := range fake.requestedPaths() {
		for _, forbidden := range []string{"submit", "cancel", "sign", "supervise"} {
			if strings.Contains(path, forbidden) {
				t.Errorf("a mutating endpoint was requested: %s", path)
			}
		}
	}
	if err := m.client.Submit(); !chaoxing.IsBlocked(err) {
		t.Errorf("Submit must always be refused, got %v", err)
	}
}

// The explicit scan fills the cache for every period, which is what lets the
// period list show how busy each one is.
func TestScanEveryPeriod(t *testing.T) {
	m, fake := newFlowModel(t)

	// Drive straight to an opened room.
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	drain(t, m, chainStep(t, m, PaneRooms), 40)
	if m.room.id == 0 || len(m.room.slots) != 2 {
		t.Fatalf("setup failed: room=%d slots=%d", m.room.id, len(m.room.slots))
	}

	// Forget one period so the scan has something to do.
	delete(m.room.occupancy, occupancyKey("19:30", "20:00"))
	if _, cached := m.room.occupancyFor("19:30", "20:00"); cached {
		t.Fatal("setup failed: the second period is still cached")
	}

	before := len(fake.requestedPaths())
	drain(t, m, m.beginScanPeriods(), 20)

	if m.room.scanning {
		t.Error("the scan did not finish")
	}
	if m.room.scanDone != len(m.room.slots) {
		t.Errorf("scan covered %d of %d periods", m.room.scanDone, len(m.room.slots))
	}
	for _, slot := range m.room.slots {
		if _, cached := m.room.occupancyFor(slot.StartTime, slot.EndTime); !cached {
			t.Errorf("period %s–%s was not cached by the scan", slot.StartTime, slot.EndTime)
		}
	}
	// One read-only query per period, and nothing else.
	if got := len(fake.requestedPaths()) - before; got != len(m.room.slots) {
		t.Errorf("the scan issued %d requests for %d periods", got, len(m.room.slots))
	}

	// The period list now reports a free count for every period.
	view := m.View()
	if !strings.Contains(view, "空闲") {
		t.Errorf("the period list does not report occupancy after a scan:\n%s", view)
	}
}

// A cancelled scan must stop issuing requests and must not report an error.
func TestScanCanBeCancelled(t *testing.T) {
	m, _ := newFlowModel(t)
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	drain(t, m, chainStep(t, m, PaneRooms), 40)
	if len(m.room.slots) == 0 {
		t.Fatalf("the fixture should offer periods, got %d", len(m.room.slots))
	}

	cmd := m.beginScanPeriods()
	if cmd == nil {
		t.Fatal("the scan did not start")
	}
	if !m.room.scanning {
		t.Fatal("the scan is not marked as running")
	}
	// esc stops it; subsequent replies are ignored by generation.
	feed(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.room.scanning {
		t.Fatal("esc did not stop the scan")
	}
	if m.room.err != nil {
		t.Errorf("cancelling a scan must not raise an error: %v", m.room.err)
	}
}

// The workflow must hold up when the QR expires and the user retries.
func TestExpiredQRCanBeRetried(t *testing.T) {
	m := newModelWithTransport(t, &apiFake{qrPNG: testQRPNG(t), qrWaiting: true})

	pollCmd := feedAll(t, m, exec(t, feed(t, m, keyMsg('r'))))
	if m.session.phase != phaseWaiting {
		t.Fatalf("phase = %v, want waiting", m.session.phase)
	}

	m.session.deadline = time.Now().Add(-time.Second)
	feedAll(t, m, exec(t, pollCmd))
	if m.session.phase != phaseExpired {
		t.Fatalf("phase = %v, want expired", m.session.phase)
	}
	if len(m.session.qrGrid) != 0 {
		t.Error("the spent code should be cleared")
	}
	if view := m.View(); !strings.Contains(view, "重新生成") {
		t.Errorf("the expired state should offer a retry:\n%s", view)
	}

	if m.zone != ZoneSide || m.side != PaneStatus {
		t.Fatalf("focus should start on the project panel, got %v", describeFocus(m))
	}
	feedAll(t, m, exec(t, feed(t, m, tea.KeyMsg{Type: tea.KeyEnter})))
	if m.session.phase != phaseWaiting || !validQRModuleCount(len(m.session.qrGrid)) {
		t.Fatalf("retry left phase=%v grids=%d", m.session.phase, len(m.session.qrGrid))
	}
}

// A rejected login must surface a classified, recoverable state rather than a raw
// error dump.
func TestRejectedLoginIsReported(t *testing.T) {
	m, fake := newFlowModel(t)
	rejecting := &apiFake{qrPNG: fake.qrPNG, badSeatPage: true}
	session, err := chaoxing.NewSessionWithConfig(chaoxing.SessionConfig{BaseTransport: rejecting})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	session.UserAgent = chaoxing.BrowserUserAgent
	m.login.Session = session

	drain(t, m, feed(t, m, keyMsg('r')), 40)

	if m.session.phase != phaseFailed {
		t.Fatalf("phase = %v, want failed", m.session.phase)
	}
	if m.session.err == nil || m.session.err.Kind != kindSession {
		t.Fatalf("expected a classified session error, got %+v", m.session.err)
	}
	view := m.View()
	if !strings.Contains(view, m.session.err.Short) {
		t.Errorf("the short error should be shown:\n%s", view)
	}
	if strings.Contains(view, "goroutine") || strings.Contains(view, "http.Client") {
		t.Error("the main view must not contain a raw Go error dump")
	}
}

// chainStep walks one step of the selection chain the way the TUI does: move to a
// panel and press space, which commits what the cursor is on and fetches the next
// step's data.
func chainStep(t *testing.T, m *Model, p Pane) tea.Cmd {
	t.Helper()
	m.focusSide(p)
	return feed(t, m, tea.KeyMsg{Type: tea.KeySpace})
}

// driveToSelection walks the real workflow to a prepared seat preview with three
// presses of space: the room, the day's first period, the seat.
func driveToSelection(t *testing.T, m *Model) {
	t.Helper()
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	drain(t, m, chainStep(t, m, PaneRooms), 40)
	if m.room.id == 0 {
		t.Fatalf("the room was not opened (err %v)", m.room.err)
	}
	drain(t, m, chainStep(t, m, PanePeriods), 40)
	if len(m.room.slots) == 0 {
		t.Fatalf("the day's periods were not loaded (err %v)", m.room.err)
	}
	drain(t, m, chainStep(t, m, PaneSeats), 40)
	if m.selection == nil {
		t.Fatalf("no selection was prepared (err %v)", m.room.err)
	}
}

// A reservation is a write, so it must be confirmed, printed in full, and sent
// exactly once. The safe choice is the one that is focused.
func TestReservationRequiresConfirmationAndSendsOnce(t *testing.T) {
	m, fake := newFlowModel(t)
	driveToSelection(t, m)

	before := len(fake.requestedPaths())
	if _, cmd := m.beginSubmit(); cmd != nil {
		t.Fatal("opening the confirmation must not send anything")
	}
	if m.overlay != OverlayConfirm {
		t.Fatalf("overlay = %v, want the confirmation", m.overlay)
	}
	view := m.View()
	for _, want := range []string{"POST", reserveTestPath, "参数", "提交预约", "选座页表单"} {
		if !strings.Contains(view, want) {
			t.Errorf("the confirmation does not show %q:\n%s", want, view)
		}
	}
	// The signature is part of the request, so the user can compare it.
	if !strings.Contains(view, "enc=") {
		t.Errorf("the confirmation does not show the signed body:\n%s", view)
	}
	if got := len(fake.requestedPaths()); got != before {
		t.Fatalf("a request was sent before confirmation (%d -> %d)", before, got)
	}

	// Focus starts on the safe choice, so a stray enter cancels.
	feed(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.overlay != OverlayNone {
		t.Fatal("enter on the safe choice must cancel the reservation")
	}
	if m.client.Current.Submitted {
		t.Fatal("cancelling must not submit")
	}
	if got := len(fake.requestedPaths()); got != before {
		t.Fatalf("cancelling sent %d request(s)", got-before)
	}

	// Confirming sends exactly one request, to the target the page named.
	if _, cmd := m.beginSubmit(); cmd != nil {
		t.Fatal("opening the confirmation sent something")
	}
	drain(t, m, feed(t, m, keyMsg('y')), 10)
	if got := fake.writesTo(reserveTestPath); got != 1 {
		t.Fatalf("reservation requests = %d, want exactly 1", got)
	}
	if !m.client.Current.Submitted || !m.selection.Submitted {
		t.Error("the service confirmed, so the selection must be marked submitted")
	}
	if m.write.result == nil || !m.write.result.OK {
		t.Fatalf("the verdict was not recorded: %+v (%v)", m.write.result, m.write.err)
	}
	// The body carries exactly the parameters that were signed.
	body := fake.bodyFor(reserveTestPath)
	for _, want := range []string{"seatNum=001", "roomId=6299", "enc="} {
		if !strings.Contains(body, want) {
			t.Errorf("request body missing %q: %s", want, body)
		}
	}
	if view := m.View(); !strings.Contains(view, "已提交") {
		t.Errorf("the details card does not report the submission:\n%s", view)
	}
}

// Waiting for a place is the way in when the room has nothing left to book, and
// it is offered in the UI rather than hidden.

// A seat page that never named a submit form cannot sign anything. The room still
// opens -- its periods and seats are readable -- and choosing a seat is a
// pre-order rather than a selection, so no write path is ever reached.
func TestPageWithoutAFormPreordersInsteadOfSigning(t *testing.T) {
	m := newModelWithTransport(t, &apiFake{qrPNG: testQRPNG(t), noForm: true})
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	resize(t, m, 130, 34)

	m.focusSide(PaneRooms)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.room.id == 0 {
		t.Fatalf("the room did not open (err %v)", m.room.err)
	}
	if m.client.CanSign() {
		t.Fatal("the page named no form, so nothing can be signed")
	}

	path := m.preorderPath()
	_ = os.Remove(path)
	m.focusSide(PaneSeats)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.selection != nil {
		t.Errorf("a seat was signed without a submit token: %+v", m.selection)
	}
	if len(m.preorders.items) != 1 {
		t.Fatalf("pre-orders = %+v, want the chosen seat remembered", m.preorders.items)
	}
	if note := m.preorders.items[0].Note; !strings.Contains(note, "提交编码") {
		t.Errorf("the pre-order does not explain why it could not be signed: %q", note)
	}

	// The submission path refuses rather than inventing a target: with nothing
	// selected it says so instead of opening a confirmation.
	m.toasts = nil
	if _, cmd := m.beginSubmit(); cmd != nil {
		t.Error("a submission was attempted with nothing selected")
	}
	if m.overlay == OverlayConfirm {
		t.Error("a submission reached the confirmation with nothing selected")
	}
	if len(m.toasts) == 0 {
		t.Error("the refusal said nothing")
	}
	if !strings.Contains(m.View(), paneTitle(PaneDetails)) {
		t.Errorf("the frame does not show the preview card:\n%s", m.View())
	}
}

// Every seat with a choice in force is marked, not just the newest one: the 预订记录
// panel lists all of them, so the seat list has to show all of them.
func TestEveryPreorderedSeatIsMarked(t *testing.T) {
	fake := &apiFake{qrPNG: testQRPNG(t), noSubmitToken: true}
	m := newModelWithTransport(t, fake)
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	resize(t, m, 130, 34)
	m.focusSide(PaneRooms)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	path := m.preorderPath()
	_ = os.Remove(path)

	// 002 is occupied and 003 is disabled, and nothing can be signed on this page:
	// both become pre-orders.
	m.focusSide(PaneSeats)
	m.focusZone(ZoneMain)
	for _, cursor := range []int{1, 2} {
		m.room.seats.cursor = cursor
		drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 10)
	}
	if len(m.preorders.items) != 2 {
		t.Fatalf("pre-orders = %+v, want both seats remembered", m.preorders.items)
	}
	committed := m.chosenSeats()
	if committed["002"] != seatPreordered || committed["003"] != seatPreordered {
		t.Fatalf("committed = %v, want both seats pre-ordered", committed)
	}
	if got := m.chosenSeat(); got != "003" {
		t.Fatalf("chosenSeat = %q, want the newest record's seat", got)
	}
	list := plainLines(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneSide && slot.owner.side == PaneSeats
	}))
	for _, number := range []string{"002", "003"} {
		if !strings.Contains(list, m.glyphs.Dot+" "+number) {
			t.Errorf("the seat list has no dot before %s:\n%s", number, list)
		}
	}
}

// Space selects and stops there: at every level, the keyboard stays on the panel the
// choice was made in. Walking on is enter's job.
func TestSpaceNeverWalksOn(t *testing.T) {
	m, _ := newFlowModel(t)
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	resize(t, m, 130, 34)

	// The room panel: space opens the room, and stays put.
	m.focusSide(PaneRooms)
	m.focusZone(ZoneSide)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.side != PaneRooms || m.zone != ZoneSide {
		t.Fatalf("space on a room moved to %v", describeFocus(m))
	}
	if m.room.id == 0 || len(m.room.slots) == 0 {
		t.Fatalf("the room did not load beside the panel (id=%d slots=%d err=%v)",
			m.room.id, len(m.room.slots), m.room.err)
	}

	// The day panel: space loads the day, and stays put.
	m.focusSide(PanePeriods)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.side != PanePeriods || m.zone != ZoneSide {
		t.Fatalf("space on a day moved to %v", describeFocus(m))
	}
	if m.selection != nil {
		t.Fatalf("choosing a day signed a seat: %+v", m.selection)
	}
	if _, cached := m.room.currentOccupancy(); !cached {
		t.Fatalf("the day's first period was not queried (err %v)", m.room.err)
	}

	// The seat list: space signs the free seat, and stays put too.
	m.focusSide(PaneSeats)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.side != PaneSeats || m.zone != ZoneSide {
		t.Fatalf("space on a seat moved to %v", describeFocus(m))
	}
	if m.selection == nil {
		t.Fatalf("the free seat was not signed (err %v)", m.room.err)
	}
	// The choice is visible where it was made, in both the list and the map.
	if view := plainLines([]string{m.View()}); !strings.Contains(view, "已选中") {
		t.Errorf("the frame does not show the signed seat:\n%s", view)
	}
}

// A plan taller than the panel scrolls with the cursor instead of hiding seats, and a
// coordinate system whose values are far apart is squeezed to their order rather than
// drawn a thousand columns wide.
func TestTallCoordinatePlanScrolls(t *testing.T) {
	m := newTestModel(t)
	populate(m)
	resize(t, m, 120, 24)

	order := make([]string, 0, 40)
	coords := map[string][2]int{}
	for row := 1; row <= 40; row++ {
		number := fmt.Sprintf("%03d", row)
		order = append(order, number)
		coords[number] = [2]int{1, row}
	}
	m.room.seatOrder = order
	m.room.seatCoords = coords
	m.room.coordSource = "x/y"
	m.room.filter = ""
	m.focusSide(PaneSeats)
	m.focusZone(ZoneMain)
	m.layout()

	plan := m.seatPlan()
	if plan == nil || plan.rows != 40 || plan.cols != 1 {
		t.Fatalf("plan = %+v, want the 40 rows the service listed", plan)
	}
	// The cursor is at the top, so the window is too.
	if lines := plainLines(m.seatPlanRows(plan, 40, 5, true)); !strings.Contains(lines, "001") {
		t.Errorf("the top of the plan is not shown at the start:\n%s", lines)
	}
	// Deep into the plan, the window follows and the seats above scroll out.
	m.focusSeatNumber("030")
	lines := plainLines(m.seatPlanRows(plan, 40, 5, true))
	if !strings.Contains(lines, "030") {
		t.Errorf("the cursor's row is not in the window:\n%s", lines)
	}
	if strings.Contains(lines, "001") {
		t.Errorf("the window did not scroll with the cursor:\n%s", lines)
	}

	// The window never runs past the plan and always contains what it is centred on.
	for _, tc := range []struct{ want, total, size, first, last int }{
		{want: 0, total: 40, size: 5, first: 0, last: 5},
		{want: 20, total: 40, size: 5, first: 18, last: 23},
		{want: 39, total: 40, size: 5, first: 35, last: 40},
		{want: 2, total: 3, size: 8, first: 0, last: 3},
		{want: 0, total: 0, size: 4, first: 0, last: 0},
	} {
		first, last := windowAround(tc.want, tc.total, tc.size)
		if first != tc.first || last != tc.last {
			t.Errorf("windowAround(%d,%d,%d) = %d..%d, want %d..%d",
				tc.want, tc.total, tc.size, first, last, tc.first, tc.last)
		}
	}

	// Values in the hundreds are squeezed to their order, and the plan says so.
	far := map[string][2]int{"001": {10, 10}, "002": {1010, 10}, "003": {10, 1010}, "004": {1010, 1010}}
	wide := buildSeatPlan([]string{"001", "002", "003", "004"}, far, "x/y")
	if wide == nil || !wide.compressed || wide.cols != 2 || wide.rows != 2 {
		t.Fatalf("a pixel-like coordinate system was not compressed: %+v", wide)
	}
	if size := m.seatPlanSize(wide); !strings.Contains(size, "压缩") {
		t.Errorf("the panel does not say the coordinates were compressed: %q", size)
	}
}

// The setting is the kill switch: with it off, no write is even offered.
func TestSubmitCanBeTurnedOffInSettings(t *testing.T) {
	m, fake := newFlowModel(t)
	driveToSelection(t, m)
	m.cfg.AllowSubmit = false

	before := len(fake.requestedPaths())
	if _, cmd := m.beginSubmit(); cmd != nil {
		t.Fatal("a disabled submission must not send anything")
	}
	if m.overlay == OverlayConfirm {
		t.Fatal("a disabled submission must not reach the confirmation")
	}
	m.focusSide(PaneRooms)

	if got := len(fake.requestedPaths()); got != before {
		t.Fatalf("a disabled write sent %d request(s)", got-before)
	}
	if m.write.err == nil {
		t.Fatal("the refusal should be recorded where the user can read it")
	}
}

// The seat detail page's own key must do what the list's key does, and the chosen
// seat must stay marked: a seat that cannot be reserved now becomes a pre-order, a
// free one becomes a selection, and either way the list shows its dot and the map
// paints it blue after the cursor has moved on.
func TestSeatDetailSpacePreordersAndMarksTheChosenSeat(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })

	m, fake := newFlowModel(t)
	driveToSelection(t, m)
	before := len(fake.requestedPaths())

	// The fixture serves 001 free, 002 occupied, 003 disabled.
	states := m.visibleSeatStates()
	if len(states) < 3 || states[2].Status == chaoxing.SeatFree {
		t.Fatalf("fixture changed: %+v", states)
	}
	path := m.preorderPath()
	_ = os.Remove(path)
	m.selection = nil

	// space in the seat detail (the map) on an occupied seat is a pre-order.
	m.focusSide(PaneSeats)
	m.focusZone(ZoneMain)
	m.room.seats.cursor = 1
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 10)
	if m.selection != nil {
		t.Fatalf("the seat detail signed a seat that cannot be signed: %+v", m.selection)
	}
	if len(m.preorders.items) != 1 || m.preorders.items[0].SeatNum != "002" {
		t.Fatalf("pre-orders = %+v, want 002 remembered from the seat detail", m.preorders.items)
	}
	if note := m.preorders.items[0].Note; !strings.Contains(note, "占用") {
		t.Errorf("the pre-order does not say why: %q", note)
	}
	if got := m.chosenSeat(); got != "002" {
		t.Fatalf("chosen seat = %q, want 002", got)
	}
	saved, err := loadPreorders(path)
	if err != nil || len(saved) != 1 || saved[0].SeatNum != "002" {
		t.Fatalf("the store holds %+v (err %v), want one record for 002", saved, err)
	}

	// The map's own line says which state the seat under the cursor is in.
	if lines := plainLines(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneMain
	})); !strings.Contains(lines, "已预订") {
		t.Errorf("the map does not say the seat was pre-ordered:\n%s", lines)
	}

	// Move the cursor off the chosen seat: the map keeps it blue and the list keeps
	// its dot, because the mark says what is in force, not where the cursor is.
	m.room.seats.cursor = 2
	rawMap := func() string {
		return strings.Join(panelLines(m, func(slot panelSlot) bool {
			return slot.owner.zone == ZoneMain
		}), "\n")
	}
	if !strings.Contains(rawMap(), m.seatCell("002", false, seatPreordered, true)) {
		t.Errorf("the pre-ordered seat is not painted in the map:\n%s", plainLines([]string{rawMap()}))
	}
	if strings.Contains(rawMap(), m.seatCell("002", false, seatUncommitted, true)) {
		t.Errorf("the pre-ordered seat is painted as if nothing was chosen")
	}
	listLines := plainLines(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneSide && slot.owner.side == PaneSeats
	}))
	if !strings.Contains(listLines, m.glyphs.Dot+" 002") {
		t.Errorf("the seat list has no dot before the chosen seat:\n%s", listLines)
	}

	// A free seat: the same key signs it instead, and that is what can be submitted.
	m.preorders.items = nil
	m.room.seats.cursor = 0
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 10)
	if m.selection == nil || m.selection.SeatNum != "001" {
		t.Fatalf("a free seat was not signed: %+v (err %v)", m.selection, m.room.err)
	}
	if m.selection.Summary().Submitted {
		t.Error("choosing a seat must not look like a submission")
	}
	if form := m.selection.PreparedForm(); !strings.Contains(form, "seatNum=001") || !strings.Contains(form, "enc=") {
		t.Errorf("the selection is not signed for the chosen seat: %s", form)
	}
	// The selection is the stronger mark -- it is the one that can be submitted.
	m.room.seats.cursor = 2
	if !strings.Contains(rawMap(), m.seatCell("001", false, seatSelected, true)) {
		t.Errorf("the signed selection is not painted as the submitted one:\n%s",
			plainLines([]string{rawMap()}))
	}

	// None of it sent anything: choosing a seat is local, and a pre-order is a file.
	if got := len(fake.requestedPaths()); got != before {
		t.Fatalf("choosing seats sent %d request(s)", got-before)
	}
}

// The scheduled shot is a simulation by construction: it fires at its moment,
// reports the request it would have sent, and never reaches the network.
func TestScheduledReserveFiresAsASimulation(t *testing.T) {
	m, fake := newFlowModel(t)
	driveToSelection(t, m)
	before := len(fake.requestedPaths())

	m.cfg.AutoLeadSeconds = 1
	if _, cmd := m.beginAutoReserve(); cmd != nil {
		t.Fatal("preparing the schedule must not send anything")
	}
	if m.overlay != OverlayConfirm {
		t.Fatalf("overlay = %v, want the confirmation", m.overlay)
	}
	confirm := m.View()
	for _, want := range []string{"模拟", "不会真实发送", reserveTestPath, "触发"} {
		if !strings.Contains(confirm, want) {
			t.Errorf("the schedule confirmation does not say %q:\n%s", want, confirm)
		}
	}

	// The safe choice is focused, so enter cancels rather than arms.
	feed(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.auto.armed {
		t.Fatal("enter on the safe choice armed the schedule")
	}

	// Confirming arms it, and nothing is sent at that point either.
	m.beginAutoReserve()
	feed(t, m, keyMsg('y'))
	if !m.auto.armed {
		t.Fatal("the schedule was not armed")
	}
	if got := len(fake.requestedPaths()); got != before {
		t.Fatalf("arming sent %d request(s)", got-before)
	}
	view := m.View()
	for _, want := range []string{"定时", "模拟"} {
		if !strings.Contains(view, want) {
			t.Errorf("the preview card does not report the schedule (%q):\n%s", want, view)
		}
	}

	// The tick past the moment fires exactly one shot.
	m.auto.at = time.Now().Add(-time.Second)
	if _, cmd := m.Update(tickMsg{at: time.Now()}); cmd == nil {
		t.Fatal("the due schedule produced no command")
	}
	if m.auto.armed {
		t.Error("the schedule stayed armed after firing")
	}
	if m.auto.shots != 1 {
		t.Fatalf("shots = %d, want 1", m.auto.shots)
	}
	if !strings.Contains(m.auto.result, "已模拟") {
		t.Errorf("the result does not report a simulation: %q", m.auto.result)
	}
	if got := len(fake.requestedPaths()); got != before {
		t.Fatalf("the simulated shot sent %d request(s)", got-before)
	}
	// A later tick must not fire it again.
	m.Update(tickMsg{at: time.Now().Add(time.Second)})
	if m.auto.shots != 1 {
		t.Fatalf("shots after a second tick = %d, want 1", m.auto.shots)
	}
	if !strings.Contains(m.View(), "已模拟") {
		t.Errorf("the preview card does not report the simulated shot:\n%s", m.View())
	}
}

// The simulated shot leaves an audit record in the same redacted log the
// diagnostics view reads.
func TestSimulatedReserveIsAudited(t *testing.T) {
	m, _ := newFlowModel(t)
	driveToSelection(t, m)

	m.cfg.AutoLeadSeconds = 1
	m.beginAutoReserve()
	feed(t, m, keyMsg('y'))
	if !m.auto.armed {
		t.Fatal("the schedule was not armed")
	}

	shot, due := m.dueAutoReserve(time.Now().Add(2 * time.Second))
	if !due {
		t.Fatal("the schedule was not due")
	}
	drain(t, m, m.fireAutoReserve(shot), 4)

	entries, _, err := loadLogEntries(m.cfg.LogDir)
	if err != nil {
		t.Fatalf("reading the log: %v", err)
	}
	var found map[string]any
	for _, entry := range entries {
		if entry.Raw == nil {
			continue
		}
		if kind, _ := entry.Raw["event"].(string); kind == "simulated_reserve" {
			found = entry.Raw
			break
		}
	}
	if found == nil {
		t.Fatalf("no simulated_reserve record in %s", m.cfg.LogDir)
	}
	if simulated, _ := found["simulated"].(bool); !simulated {
		t.Errorf("the record does not say it was simulated: %+v", found)
	}
	if seat, _ := found["seat"].(string); seat != "001" {
		t.Errorf("the record names seat %q, want 001", seat)
	}
	if path, _ := found["path"].(string); path != reserveTestPath {
		t.Errorf("the record names path %q, want %q", path, reserveTestPath)
	}
}

// esc cancels an armed schedule, which is the only way out of a pending shot.
func TestEscapeCancelsAScheduledReserve(t *testing.T) {
	m, fake := newFlowModel(t)
	driveToSelection(t, m)
	before := len(fake.requestedPaths())

	m.cfg.AutoLeadSeconds = 600
	m.beginAutoReserve()
	feed(t, m, keyMsg('y'))
	if !m.auto.armed {
		t.Fatal("the schedule was not armed")
	}
	feed(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.auto.armed {
		t.Fatal("esc did not cancel the schedule")
	}
	if !strings.Contains(m.auto.result, "取消") {
		t.Errorf("the cancellation is not reported: %q", m.auto.result)
	}
	// Cancelling is not a request, and it must not have changed the level either.
	if got := len(fake.requestedPaths()); got != before {
		t.Fatalf("cancelling sent %d request(s)", got-before)
	}
	if m.side != PaneSeats || m.zone != ZoneSide {
		t.Fatalf("esc that cancelled a schedule also moved focus to %v", describeFocus(m))
	}
}

// A period that has not been queried must say so where the user is looking: the
// seat list is static, so the report beside it is what explains the missing
// occupancy, and a panel one row tall still has to carry the action.
func TestUnqueriedPeriodExplainsItselfInShortPanels(t *testing.T) {
	m, _ := newFlowModel(t)
	driveToSelection(t, m)
	// Forget the period; the static list is unaffected, the report is not.
	m.room.clearOccupancy()
	m.focusSide(PaneSeats)
	m.zone = ZoneMain
	resize(t, m, 80, 24)
	m.layout()

	rows := strings.Join(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneMain
	}), "\n")
	if !strings.Contains(rows, "未查询") {
		t.Errorf("the seat report does not explain the missing occupancy:\n%s", rows)
	}
	if !strings.Contains(rows, "只读") && !strings.Contains(rows, "enter") {
		t.Errorf("the report carries neither a read-only mark nor the action:\n%s", rows)
	}
	// And the seat list itself stays static and complete.
	list := strings.Join(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneSide && slot.owner.side == PaneSeats
	}), "\n")
	if !strings.Contains(list, "001") {
		t.Errorf("the static seat list lost its numbers:\n%s", list)
	}
}

// Opening a room that fails must report the failure in the content panel, because
// that is where the keyboard lands: a silent detail card looks like "no seats".
func TestFailedRoomOpenIsReportedInTheContent(t *testing.T) {
	m, fake := newFlowModel(t)
	driveToSelection(t, m)

	fake.mu.Lock()
	fake.badSeatPage = false
	fake.mu.Unlock()

	// Make the next room open fail as a policy refusal and re-run it.
	m.focusSide(PaneRooms)
	m.room.err = classify(&chaoxing.BlockedSeatRequest{Reason: "已在发送前拦截非只读座位接口"})
	m.room.id = 6299
	m.room.slots = nil
	m.zone = ZoneMain
	m.layout()

	view := m.View()
	if !strings.Contains(view, "只读策略已拦截该请求") {
		t.Errorf("the content does not report the failed open:\n%s", view)
	}
	roomRows := strings.Join(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneMain
	}), "\n")
	if !strings.Contains(roomRows, "只读策略已拦截该请求") {
		t.Errorf("the room detail card hides the failure:\n%s", roomRows)
	}
	// And the periods it cannot list are explained as a failure, not as an empty
	// day, which is what the queue hint says.
	if !strings.Contains(view, "打开失败") {
		t.Errorf("the period panel does not report the failure:\n%s", view)
	}
}

// The state an evening session is actually in: the server's day has no periods
// left, so there is nothing to select and nothing to schedule. The way out is the
// next day, whose window is not open yet -- which is exactly what a scheduled
// request is for. This walks the whole path: see the dead end, step the date,
// open the room again, plan a seat, schedule the shot, and watch it fire as a
// simulation.
func TestPastDayIsRescuedByPlanningTomorrow(t *testing.T) {
	fake := &apiFake{qrPNG: testQRPNG(t), pastToday: true}
	m := newModelWithTransport(t, fake)

	// Log in and open a room on the server's own day.
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	drain(t, m, chainStep(t, m, PaneRooms), 40)
	if m.room.id == 0 || len(m.room.slots) != 0 {
		t.Fatalf("expected a room with no periods left, got id=%d slots=%d",
			m.room.id, len(m.room.slots))
	}
	// The dead end explains itself instead of looking empty.
	view := m.View()
	for _, want := range []string{"没有未来可预约时段", "]"} {
		if !strings.Contains(view, want) {
			t.Errorf("the dead end does not explain itself (%q):\n%s", want, view)
		}
	}

	// Step to the next day: the room list reloads for it.
	before := len(fake.requestedPaths())
	drain(t, m, feed(t, m, keyMsg(']')), 40)
	if m.rooms.day != "2026-09-17" {
		t.Fatalf("day after ] = %q, want 2026-09-17", m.rooms.day)
	}
	if len(fake.requestedPaths()) <= before {
		t.Fatal("stepping the date did not reload the room list")
	}
	// The room is reopened on the new day on purpose: choosing a day is meant to
	// land on that day's periods, and the old preview is gone with the old day.
	if m.selection != nil {
		t.Fatalf("the previous day's preview survived the date change: %v", m.selection)
	}
	if len(m.room.slots) == 0 {
		t.Fatalf("the room was not reopened on the new day (id=%d err=%v)", m.room.id, m.room.err)
	}

	// Open a room on the new day: its periods are listed even though the window
	// is not open yet, which is the whole point.
	drain(t, m, chainStep(t, m, PaneRooms), 40)
	if len(m.room.slots) == 0 {
		t.Fatalf("tomorrow has no periods either (err %v)", m.room.err)
	}
	if len(m.visibleSeatStates()) == 0 {
		t.Fatalf("tomorrow has no seats to plan (err %v)", m.room.err)
	}
	if _, opensAt, ok := m.client.ReserveWindow(); !ok || opensAt.IsZero() {
		t.Fatalf("the page's opening time was not captured: %v %v", opensAt, ok)
	}

	// Plan a seat with the chain, then schedule the shot.
	drain(t, m, chainStep(t, m, PaneSeats), 40)
	if m.selection == nil {
		t.Fatalf("no plan was prepared on the new day (err %v)", m.room.err)
	}
	writes := len(fake.requestedPaths())
	m.beginAutoReserve()
	if m.overlay != OverlayConfirm {
		t.Fatalf("overlay = %v, want the schedule confirmation", m.overlay)
	}
	// The trigger is the server's opening time, not the configured lead.
	confirm := m.View()
	if !strings.Contains(confirm, "预约窗口开放时刻") {
		t.Errorf("the confirmation does not aim at the window opening:\n%s", confirm)
	}
	feed(t, m, keyMsg('y'))
	if !m.auto.armed {
		t.Fatal("the schedule was not armed")
	}

	// Fire it: a simulation, so the request count must not move.
	m.auto.at = time.Now().Add(-time.Second)
	if _, cmd := m.Update(tickMsg{at: time.Now()}); cmd == nil {
		t.Fatal("the due schedule produced no command")
	}
	if !strings.Contains(m.auto.result, "已模拟") {
		t.Errorf("the shot was not reported as a simulation: %q", m.auto.result)
	}
	if got := len(fake.requestedPaths()); got != writes {
		t.Fatalf("the simulated shot sent %d request(s)", got-writes)
	}

	// [ steps back, and D hands the date to the server again.
	drain(t, m, feed(t, m, keyMsg('[')), 40)
	if m.rooms.day != seatDay {
		t.Fatalf("[ left the day as %q, want %s", m.rooms.day, seatDay)
	}
	drain(t, m, feed(t, m, keyMsg('D')), 40)
	if m.rooms.day != m.days.anchor {
		t.Fatalf("D left the day as %q, want the server's own %q", m.rooms.day, m.days.anchor)
	}
	if m.days.cursor != 0 {
		t.Fatalf("D left the planner cursor at %d, want the first day", m.days.cursor)
	}
}

// firstNonEmpty returns the first non-empty value, which lets the fixture read a
// parameter wherever the client put it.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// The period panel is a day planner and the content beside it tiles the day's
// periods, so a whole day can be read at once. Every cell carries the time, the
// free count against the room's capacity and a bar; movement stays linear.
func TestDayPlannerAndPeriodGrid(t *testing.T) {
	fake := &apiFake{qrPNG: testQRPNG(t), manyPeriods: true}
	m := newModelWithTransport(t, fake)
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	drain(t, m, chainStep(t, m, PaneRooms), 40)
	if got := len(m.room.slots); got < 2 {
		t.Fatalf("the fixture should offer several periods, got %d", got)
	}
	m.focusSide(PanePeriods)
	m.zone = ZoneSide
	resize(t, m, 150, 40)
	m.layout()

	// The planner always has eight rows: seven days and the cron expression.
	planRows := panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneSide && slot.owner.side == PanePeriods
	})
	plan := strings.Join(planRows, "\n")
	for _, want := range []string{"今天", "明天"} {
		if !strings.Contains(plan, want) {
			t.Errorf("the planner is missing %q:\n%s", want, plan)
		}
	}
	// Every row is a day: the planner is seven days.
	m.setDayRow(periodRows - 1)
	if got := strings.Join(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneSide && slot.owner.side == PanePeriods
	}), "\n"); !strings.Contains(got, "自动") {
		t.Errorf("the planner is missing its cron row:\n%s", got)
	}
	m.setDayRow(0)

	// The content tiles the day: several periods side by side, one row per band.
	m.zone = ZoneMain
	m.layout()
	grid := strings.Join(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneMain
	}), "\n")
	if got := strings.Count(grid, m.glyphs.Border.TopLeft); got != 1 {
		t.Errorf("expected only the outer panel border, got %d", got)
	}
	for _, want := range []string{"19:00–19:30", "20:00–20:30", "空闲"} {
		if !strings.Contains(grid, want) {
			t.Errorf("the grid is missing %q:\n%s", want, grid)
		}
	}

	// The grid takes both axes: l is the next period in the row, j is the same
	// column one row down.
	first := m.room.slotIndex
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.room.slotIndex != first+1 {
		t.Fatalf("l moved the period cursor to %d, want %d", m.room.slotIndex, first+1)
	}
	columns := max(m.gridCols, 1)
	if len(m.room.slots) > columns {
		m.selectSlot(0)
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
		if m.room.slotIndex != columns {
			t.Fatalf("j moved the period cursor to %d, want one row down (%d)",
				m.room.slotIndex, columns)
		}
	}
	// space queries the period it is on, and enter moves on to the seats.
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace}); cmd == nil {
		t.Fatal("space on a period produced no query")
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatal("enter on the period grid must move on, not act")
	}
	if m.side != PaneSeats || m.zone != ZoneMain {
		t.Fatalf("enter on the period grid gave %v/%v, want the seat map",
			m.side.title(), m.zone)
	}
}

// The right column of the seat panel is a map, so it takes both movement axes:
// h/l step within a row, j/k step between rows, and esc -- not h -- is what
// returns to the panel group.
func TestSeatMapTakesBothAxesAndEscapeReturns(t *testing.T) {
	fake := &apiFake{qrPNG: testQRPNG(t), manyPeriods: true}
	m := newModelWithTransport(t, fake)
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	drain(t, m, chainStep(t, m, PaneRooms), 40)
	resize(t, m, 120, 30)

	// Open the map from the seat panel with enter, which previews rather than
	// committing, and fetches the period it needs to draw.
	feed(t, m, keyMsg('3'))
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeyEnter}), 40)
	if m.zone != ZoneMain {
		t.Fatalf("enter on the seat panel gave %v, want the map", describeFocus(m))
	}
	if len(m.visibleSeatNumbers()) < 3 {
		t.Fatalf("the fixture should offer seats, got %d", len(m.visibleSeatNumbers()))
	}
	columns := max(m.gridCols, 1)
	if columns < 2 {
		t.Fatalf("the map fitted %d column(s), want several at 120 columns", columns)
	}

	// h/l move within the row, and do not leave the panel.
	start := m.room.seats.cursor
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.room.seats.cursor != start+1 {
		t.Fatalf("l moved the map cursor to %d, want %d", m.room.seats.cursor, start+1)
	}
	if m.zone != ZoneMain {
		t.Fatalf("l left the map for %v; esc is what returns", describeFocus(m))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if m.room.seats.cursor != start {
		t.Fatalf("h did not step back within the row (cursor %d)", m.room.seats.cursor)
	}
	// j/k change row, which is a whole row of seats away.
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if want := start + columns; m.room.seats.cursor != want {
		t.Fatalf("j moved the map cursor to %d, want one row down (%d)", m.room.seats.cursor, want)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.room.seats.cursor != start {
		t.Fatalf("k did not step back up (cursor %d)", m.room.seats.cursor)
	}

	// The selection is shared with the linear list, so both views agree.
	if number, _ := m.selectedSeatNumber(); number == "" {
		t.Fatal("the map has no selected seat")
	}

	// esc returns to the panel group, and the panel stays the seat panel.
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.zone != ZoneSide || m.side != PaneSeats {
		t.Fatalf("esc gave %v/%v, want the seat panel", m.zone, m.side.title())
	}
	// From the panel group h/l still choose the panel.
	m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if m.side == PaneSeats {
		t.Fatal("h in the panel group did not change the panel")
	}
}

// The right-hand grids are pickers, so both space and enter select what the
// cursor is on, and the choice is confirmed out loud -- pressing enter on an
// already-queried period used to leave the screen completely unchanged.
func TestSpaceAndEnterSelectInTheGrids(t *testing.T) {
	fake := &apiFake{qrPNG: testQRPNG(t)}
	m := newModelWithTransport(t, fake)
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	drain(t, m, chainStep(t, m, PaneRooms), 40)
	resize(t, m, 120, 30)

	// The period grid: space commits the period and says so.
	feed(t, m, keyMsg('2'))
	feed(t, m, tea.KeyMsg{Type: tea.KeyEnter}) // into the grid
	if m.zone != ZoneMain {
		t.Fatalf("enter on the day planner gave %v, want the period grid", describeFocus(m))
	}
	m.toasts = nil
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 10)
	if len(m.toasts) == 0 {
		t.Fatal("space on the period grid said nothing")
	}
	if got := m.toasts[len(m.toasts)-1].text; !strings.Contains(got, "已选定时段") {
		t.Errorf("space on the period grid reported %q", got)
	}

	// The seat map: space selects, and enter moves on to the prepared preview.
	for _, tc := range []struct {
		key  tea.KeyMsg
		want bool
	}{
		{tea.KeyMsg{Type: tea.KeySpace}, true},
		{tea.KeyMsg{Type: tea.KeyEnter}, false},
	} {
		m.selection = nil
		feed(t, m, keyMsg('3'))
		drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeyEnter}), 40) // preview the map
		drain(t, m, feed(t, m, tc.key), 40)
		if (m.selection != nil) != tc.want {
			t.Fatalf("%v on the seat map: selection=%v, want %v (err %v)",
				tc.key.Type, m.selection != nil, tc.want, m.room.err)
		}
		if tc.want {
			if got := m.selection.SeatNum; got != "001" {
				t.Errorf("%v selected seat %s, want the one under the cursor", tc.key.Type, got)
			}
		} else if m.zone != ZonePreview {
			t.Errorf("enter on the seat map gave %v, want the prepared preview", describeFocus(m))
		}
	}
}

// Selecting a seat whose period has not been queried is one key press: the query
// is started, and the selection is completed by its reply.
func TestSelectingASeatBeforeItsPeriodIsQueried(t *testing.T) {
	fake := &apiFake{qrPNG: testQRPNG(t)}
	m := newModelWithTransport(t, fake)
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	drain(t, m, chainStep(t, m, PaneRooms), 40)
	resize(t, m, 120, 30)

	// Forget the period: the map is drawn light, and there is nothing to judge a
	// seat by until the query comes back.
	m.room.clearOccupancy()
	m.room.pendingSeat = ""
	m.selection = nil
	feed(t, m, keyMsg('3'))
	// The map's enter starts the query and puts the keyboard on the map; the user
	// selects before the query lands, which is the interleaving this path exists
	// for.
	query := feed(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.zone != ZoneMain {
		t.Fatalf("enter on the seat panel gave %v, want the map", describeFocus(m))
	}
	selected := feed(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if _, cached := m.room.currentOccupancy(); cached {
		t.Fatal("setup: the period should not be cached")
	}
	if m.room.pendingSeat == "" {
		t.Fatal("selecting before the query landed was not remembered")
	}

	drain(t, m, query, 40)
	drain(t, m, selected, 40)
	if m.room.pendingSeat != "" {
		t.Errorf("the pending selection was not completed: %q", m.room.pendingSeat)
	}
	if m.selection == nil {
		t.Fatalf("the seat was not selected after its period came back (err %v)", m.room.err)
	}
	if !strings.Contains(m.View(), "已准备") {
		t.Errorf("the preview card does not report the selection:\n%s", m.View())
	}
}

// Choosing another date must not throw the room away: the room's identity
// survives, only its periods are reloaded for the new day. Losing it read as "my
// room disappeared".
func TestChangingTheDateKeepsTheRoom(t *testing.T) {
	fake := &apiFake{qrPNG: testQRPNG(t), manyPeriods: true}
	m := newModelWithTransport(t, fake)
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	resize(t, m, 130, 34)

	// Commit a room on today.
	m.focusSide(PaneRooms)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	roomID, roomName := m.room.id, m.room.name
	if roomID == 0 {
		t.Fatalf("no room was opened (err %v)", m.room.err)
	}

	// Choose tomorrow from the day planner.
	m.focusSide(PanePeriods)
	feed(t, m, keyMsg('j'))
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)

	if m.rooms.day != "2026-09-17" {
		t.Fatalf("day = %q, want tomorrow", m.rooms.day)
	}
	// The room is still the chosen one, still marked, and its periods are the
	// new day's.
	if m.room.id != roomID || m.room.name != roomName {
		t.Fatalf("the room was lost on the date change: id=%d name=%q (want %d/%q)",
			m.room.id, m.room.name, roomID, roomName)
	}
	rows := strings.Join(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneSide && slot.owner.side == PaneRooms
	}), "\n")
	if !strings.Contains(rows, m.glyphs.Dot+" "+itoa(roomID)) {
		t.Errorf("the chosen room lost its dot on the date change:\n%s", rows)
	}
	if len(m.room.slots) == 0 {
		t.Fatalf("the new day's periods were not loaded (err %v)", m.room.err)
	}
	// The preview belonged to the old day, so it is gone -- and said so.
	if m.selection != nil {
		t.Errorf("a preview signed for the old day survived: %+v", m.selection)
	}
}

// Only the panel that owns the keyboard paints a selection bar. A dimmed bar in
// an inactive panel still competes with the active one, which makes the keyboard's
// position hard to find.
func TestOnlyTheActivePanelHighlightsARow(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })

	m := newTestModel(t)
	populate(m)
	resize(t, m, 130, 34)

	probe := m.theme.RowSelected.Render("X")
	bar := probe[:strings.Index(probe, "X")]
	if bar == "" {
		t.Skip("the selection bar renders without an escape sequence")
	}

	// The active panel's selected row carries the bar.
	focusPanel(m, PaneRooms)
	focused := strings.Join(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneSide && slot.owner.side == PaneRooms
	}), "\n")
	if !strings.Contains(focused, bar) {
		t.Errorf("the active panel's selected row has no highlight:\n%s", focused)
	}

	// An inactive panel's rows do not, however many of them are selected.
	focusPanel(m, PaneSeats)
	inactive := strings.Join(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneSide && slot.owner.side == PaneRooms
	}), "\n")
	if strings.Contains(inactive, bar) {
		t.Errorf("an inactive panel painted a selection bar:\n%s", inactive)
	}
	// And the inactive panel keeps a faint marker, so the row is still findable.
	if !strings.Contains(inactive, m.glyphs.Selected) {
		t.Errorf("the inactive panel lost its cursor marker:\n%s", inactive)
	}
}

// A future day has no submit token, because its reservation window is not open
// yet. That must not stop the room from opening: its periods and its seats are
// perfectly readable, and choosing one is a pre-order.
func TestFutureDayOpensAndSeatsCanBePreordered(t *testing.T) {
	fake := &apiFake{qrPNG: testQRPNG(t), manyPeriods: true}
	m := newModelWithTransport(t, fake)
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	resize(t, m, 130, 34)

	// Commit a room on today, then walk to tomorrow: the seat page serves no
	// submit token for a day whose window has not opened.
	m.focusSide(PaneRooms)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.room.id == 0 {
		t.Fatalf("today's room did not open (err %v)", m.room.err)
	}
	fake.noSubmitToken = true
	m.focusSide(PanePeriods)
	feed(t, m, keyMsg('j'))
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.rooms.day != "2026-09-17" {
		t.Fatalf("day = %q, want tomorrow", m.rooms.day)
	}
	if m.room.id == 0 {
		t.Fatalf("tomorrow's room did not open (err %v)", m.room.err)
	}
	if len(m.room.slots) == 0 {
		t.Fatalf("tomorrow's periods were not listed (err %v)", m.room.err)
	}
	// Every seat of the room is listed, whatever the occupancy query returned.
	if got, want := len(m.visibleSeatNumbers()), m.staticSeatCount(); got != want || want == 0 {
		t.Fatalf("seat list has %d of %d seats", got, want)
	}

	// Choosing a seat from an unsigned day records a pre-order.
	m.focusSide(PaneSeats)
	seat, _ := m.selectedSeatNumber()
	path := m.preorderPath()
	_ = os.Remove(path)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)

	if m.selection != nil {
		t.Errorf("an unsigned day produced a signed selection: %+v", m.selection)
	}
	if len(m.preorders.items) != 1 {
		t.Fatalf("pre-orders = %d, want 1 (%v)", len(m.preorders.items), m.preorders.err)
	}
	item := m.preorders.items[0]
	if item.SeatNum != seat || item.Day != "2026-09-17" {
		t.Fatalf("the pre-order records the wrong seat: %+v (want %s on 2026-09-17)", item, seat)
	}
	if item.RoomID != m.room.id {
		t.Errorf("the pre-order records room %d, want %d", item.RoomID, m.room.id)
	}

	// It is written into the project's own memory, and readable again.
	saved, err := loadPreorders(path)
	if err != nil {
		t.Fatalf("reading the store: %v", err)
	}
	if len(saved) != 1 || saved[0].SeatNum != seat {
		t.Fatalf("the store holds %+v, want the pre-order for %s", saved, seat)
	}

	// The panel lists it, and space cancels it after a confirmation.
	feed(t, m, keyMsg('4'))
	rows := strings.Join(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneSide && slot.owner.side == PaneRecords
	}), "\n")
	if !strings.Contains(rows, seat) {
		t.Errorf("the pre-order panel does not list %s:\n%s", seat, rows)
	}
	feed(t, m, tea.KeyMsg{Type: tea.KeySpace})
	if m.overlay != OverlayConfirm {
		t.Fatalf("space on a pre-order gave %v, want the confirmation", overlayName(m.overlay))
	}
	if confirm := m.View(); !strings.Contains(confirm, "取消预订") {
		t.Errorf("the confirmation does not name the action:\n%s", confirm)
	}
	drain(t, m, feed(t, m, keyMsg('y')), 10)
	if len(m.preorders.items) != 0 {
		t.Fatalf("the pre-order survived its cancellation: %+v", m.preorders.items)
	}
	after, err := loadPreorders(path)
	if err != nil {
		t.Fatalf("re-reading the store: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("the store still holds %+v after cancellation", after)
	}
}

// A seat that can be taken right now is a selection, not a pre-order: the same
// key means "sign it" when that is possible and "remember it" when it is not.
func TestFreeSeatIsSelectedAndTakenSeatIsPreordered(t *testing.T) {
	fake := &apiFake{qrPNG: testQRPNG(t)}
	m := newModelWithTransport(t, fake)
	driveToSelection(t, m)

	// The fixture serves 001 free, 002 occupied, 003 disabled.
	m.focusSide(PaneSeats)
	if got := m.selection.SeatNum; got != "001" {
		t.Fatalf("setup: the selection is for %s", got)
	}
	path := m.preorderPath()
	_ = os.Remove(path)

	// Seat 002 is occupied now, so space on it is a pre-order.
	m.room.seats.cursor = 1
	m.selection = nil
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.selection != nil {
		t.Errorf("an occupied seat was signed instead of pre-ordered: %+v", m.selection)
	}
	if len(m.preorders.items) != 1 || m.preorders.items[0].SeatNum != "002" {
		t.Fatalf("pre-orders = %+v, want one for 002", m.preorders.items)
	}
	if note := m.preorders.items[0].Note; !strings.Contains(note, "占用") {
		t.Errorf("the pre-order does not say why: %q", note)
	}

	// Seat 001 is free and the page can sign, so space selects it again.
	m.focusSide(PaneSeats)
	m.room.seats.cursor = 0
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.selection == nil || m.selection.SeatNum != "001" {
		t.Fatalf("a free seat was not selected: %+v (%v)", m.selection, m.room.err)
	}
	if len(m.preorders.items) != 1 {
		t.Errorf("selecting a free seat changed the pre-orders: %+v", m.preorders.items)
	}
}

// The real service does not serve the select page before a day's window opens: it
// answers with a redirect or an error. The room must still open -- its periods and
// its whole seat numbering come from room/info -- and its seats must be bookable
// as pre-orders.
func TestRoomOpensEvenWhenTheSelectPageRefuses(t *testing.T) {
	for _, status := range []int{302, 403, 500} {
		fake := &apiFake{qrPNG: testQRPNG(t), manyPeriods: true, selectPageStatus: status}
		m := newModelWithTransport(t, fake)
		drain(t, m, feed(t, m, keyMsg('r')), 10)
		drain(t, m, m.cmdPollTick(m.genSession), 40)
		resize(t, m, 130, 34)

		m.focusSide(PaneRooms)
		drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
		if m.room.id == 0 {
			t.Fatalf("HTTP %d from the select page stopped the room from opening (err %v)",
				status, m.room.err)
		}
		if len(m.room.slots) == 0 {
			t.Fatalf("HTTP %d: the periods were not listed", status)
		}
		if got, want := len(m.visibleSeatNumbers()), m.staticSeatCount(); got != want || want == 0 {
			t.Fatalf("HTTP %d: seat list has %d of %d seats", status, got, want)
		}
		if m.client.CanSign() {
			t.Fatalf("HTTP %d: the page refused, so nothing can be signed", status)
		}
		if note := m.client.SignNote(); !strings.Contains(note, "HTTP") {
			t.Errorf("HTTP %d: the reason is not reported: %q", status, note)
		}
		// And the panel says the seats can be pre-ordered rather than looking dead.
		view := m.View()
		if !strings.Contains(view, "可预订") {
			t.Errorf("HTTP %d: the frame does not offer pre-ordering:\n%s", status, view)
		}

		path := m.preorderPath()
		_ = os.Remove(path)
		m.focusSide(PaneSeats)
		seat, _ := m.selectedSeatNumber()
		drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
		if m.selection != nil {
			t.Errorf("HTTP %d: a seat was signed without a page: %+v", status, m.selection)
		}
		if len(m.preorders.items) != 1 || m.preorders.items[0].SeatNum != seat {
			t.Fatalf("HTTP %d: pre-orders = %+v, want one for %s",
				status, m.preorders.items, seat)
		}
	}
}

// A day whose room/info repeats less than the day that was open before must still
// list every seat: the numbering is room metadata, not a daily fact.
func TestThinFutureDayStillListsEverySeat(t *testing.T) {
	fake := &apiFake{qrPNG: testQRPNG(t), thinInfoDay: "2026-09-17"}
	m := newModelWithTransport(t, fake)
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	resize(t, m, 130, 34)

	m.focusSide(PaneRooms)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	want := m.staticSeatCount()
	if want == 0 || m.room.id == 0 {
		t.Fatalf("no room opened on the server's day (id=%d seats=%d err=%v)",
			m.room.id, want, m.room.err)
	}

	// Tomorrow answers with no capacity and no startSeatNum at all.
	m.focusSide(PanePeriods)
	feed(t, m, keyMsg('j'))
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)

	if m.room.err != nil {
		t.Fatalf("the thin day failed to open: %v", m.room.err)
	}
	if got := m.staticSeatCount(); got != want {
		t.Fatalf("seats on the thin day = %d, want the room's %d", got, want)
	}
	numbers := m.visibleSeatNumbers()
	if len(numbers) != want {
		t.Fatalf("the thin day's seat list = %v, want all %d seats", numbers, want)
	}
	// A list-mode room has no listing: its numbering is the order, and there is no
	// server listing to report either.
	if len(m.room.seatOrder) != 0 {
		t.Fatalf("a list-mode room claimed a listing: %v", m.room.seatOrder)
	}
	if len(m.client.GridKeys()) != 0 {
		t.Fatalf("a list-mode room reported listing fields: %v", m.client.GridKeys())
	}
	rows := strings.Join(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneSide && slot.owner.side == PaneSeats
	}), "\n")
	for _, number := range numbers {
		if !strings.Contains(rows, number) {
			t.Errorf("the thin day's seat %s is missing from the list:\n%s", number, rows)
		}
	}
}

// A day that refuses to open must not blank the room either: the seats stay
// listed, the failure is reported beside them, and choosing one is still a
// pre-order -- with no period, because the refused day has none.
func TestRefusedDayKeepsTheSeatsAndStillPreorders(t *testing.T) {
	fake := &apiFake{qrPNG: testQRPNG(t), failInfoDay: "2026-09-17"}
	m := newModelWithTransport(t, fake)
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	resize(t, m, 130, 34)

	m.focusSide(PaneRooms)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	want := m.staticSeatCount()
	if want == 0 || m.room.id == 0 {
		t.Fatalf("no room opened on the server's day (id=%d seats=%d err=%v)",
			m.room.id, want, m.room.err)
	}

	m.focusSide(PanePeriods)
	feed(t, m, keyMsg('j'))
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.room.err == nil {
		t.Fatal("the refused day opened anyway")
	}
	if got := m.staticSeatCount(); got != want {
		t.Fatalf("the refused day lost the seats: %d of %d", got, want)
	}
	if len(m.visibleSeatNumbers()) != want {
		t.Fatalf("the refused day's seat list = %v, want %d seats", m.visibleSeatNumbers(), want)
	}
	status := m.seatsStatusLine()
	if !strings.Contains(status, "列出") || !strings.Contains(status, "打开失败") {
		t.Errorf("the seat panel does not report the failure beside the list: %q", status)
	}
	// The keyboard stayed on the day planner, where the failure was reported; the
	// seat panel itself still shows the seats and explains the failure when opened.
	feed(t, m, keyMsg('3'))
	if view := plainLines([]string{m.View()}); !strings.Contains(view, "下列为已知座位") {
		t.Errorf("the seat detail replaced the room's seats with the error:\n%s", view)
	}

	path := m.preorderPath()
	_ = os.Remove(path)
	m.focusSide(PaneSeats)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if len(m.preorders.items) != 1 {
		t.Fatalf("pre-orders = %+v, want the chosen seat remembered", m.preorders.items)
	}
	item := m.preorders.items[0]
	if item.SeatNum == "" || item.Start != "" || item.End != "" {
		t.Fatalf("pre-order = %+v, want a seat with no period on a refused day", item)
	}
	// The record itself explains the missing period, and the panel that lists it
	// shows that text when the user goes there.
	feed(t, m, keyMsg('4'))
	if view := plainLines([]string{m.View()}); !strings.Contains(view, "全时段（当日未开放）") {
		t.Errorf("the record does not explain its missing period:\n%s", view)
	}
}

// A future day whose seat query is refused must still list every seat: the room's
// numbering is not the query's answer. The failure is reported beside the list, and
// the seat that could not be judged is pre-ordered with its period, not signed.
func TestRefusedSeatQueryKeepsTheSeatsListed(t *testing.T) {
	fake := &apiFake{qrPNG: testQRPNG(t), failSeatsDay: "2026-09-17"}
	m := newModelWithTransport(t, fake)
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	resize(t, m, 130, 34)

	m.focusSide(PaneRooms)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	want := m.staticSeatCount()
	if want == 0 || m.room.id == 0 {
		t.Fatalf("no room opened on the server's day (id=%d seats=%d err=%v)",
			m.room.id, want, m.room.err)
	}

	m.focusSide(PanePeriods)
	feed(t, m, keyMsg('j'))
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.room.err == nil {
		t.Fatal("the refused seat query was not reported")
	}
	if got := m.staticSeatCount(); got != want {
		t.Fatalf("the refused query cost the room its seats: %d of %d", got, want)
	}
	status := m.seatsStatusLine()
	if !strings.Contains(status, "列出") || !strings.Contains(status, "查询失败") {
		t.Errorf("the seat panel does not report the query beside the list: %q", status)
	}
	// The keyboard stayed on the day planner, where the failure was reported; the
	// seat panel itself still shows the seats and explains the failure when opened.
	feed(t, m, keyMsg('3'))
	if view := plainLines([]string{m.View()}); !strings.Contains(view, "下列为已知座位") {
		t.Errorf("the seat detail replaced the room's seats with the error:\n%s", view)
	}

	path := m.preorderPath()
	_ = os.Remove(path)
	m.focusSide(PaneSeats)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.selection != nil {
		t.Errorf("a seat was signed although nothing could be said about it: %+v", m.selection)
	}
	if len(m.preorders.items) != 1 {
		t.Fatalf("pre-orders = %+v, want the chosen seat remembered", m.preorders.items)
	}
	if item := m.preorders.items[0]; item.Start != "19:00" || item.End != "19:30" {
		t.Fatalf("pre-order = %+v, want the queried day's period", item)
	}
}

// A grid-mode room is the only kind the service sends a seat listing for, so it is
// the only place a seat's position could come from. Whatever fields that listing
// carries must be reported, not assumed -- and the map still goes by seat number.
func TestGridRoomReportsTheFieldsTheServiceSent(t *testing.T) {
	fake := &apiFake{qrPNG: testQRPNG(t), gridRoom: true}
	m := newModelWithTransport(t, fake)
	drain(t, m, feed(t, m, keyMsg('r')), 10)
	drain(t, m, m.cmdPollTick(m.genSession), 40)
	resize(t, m, 130, 34)

	m.focusSide(PaneRooms)
	drain(t, m, feed(t, m, tea.KeyMsg{Type: tea.KeySpace}), 40)
	if m.room.id == 0 {
		t.Fatalf("the grid room did not open (err %v)", m.room.err)
	}
	if got := strings.Join(m.client.GridKeys(), ","); got != "reserveStatus,seatNum,x,y" {
		t.Fatalf("reported grid fields = %q, want the listing's own fields", got)
	}
	if got := strings.Join(m.client.GridCoordKeys(), ","); got != "x,y" {
		t.Fatalf("reported coordinate fields = %q, want x,y", got)
	}

	// A grid room's seats are the service's own listing, in the service's own
	// order: that listing is what the room draws itself from.
	numbers := m.visibleSeatNumbers()
	if got := strings.Join(numbers, ","); got != "003,001,004,002" {
		t.Fatalf("seat list = %v, want the listing's own order", numbers)
	}
	if m.seatNumberAt(0) != "003" || m.seatNumberAt(3) != "002" {
		t.Fatalf("the seat list is not in the listing's order: %v", numbers)
	}
	if m.room.layout.Mode != "网格模式" || m.room.layout.Grid != 4 {
		t.Fatalf("layout = %+v, want the grid room's 4 listed seats", m.room.layout)
	}
	// Position still comes from the service's listing, not from the numbering.
	spot, ok := m.seatSpot("003")
	if !ok || spot.GridOrdinal != 1 || spot.GridTotal != 4 {
		t.Fatalf("seat 003 = %+v ok=%v, want listing position 1 of 4", spot, ok)
	}
	if spot.CoordSource != "x/y" || spot.Col != 1 || spot.Row != 2 {
		t.Fatalf("seat 003 position = %+v, want the service's x=1 y=2", spot)
	}
	if text := seatSpotText(spot); !strings.Contains(text, "x=1 y=2") {
		t.Errorf("the position is not reported as a coordinate: %q", text)
	}

	// The map is arranged by those coordinates, aisle included: 003 and 001 share a
	// row, and the column the service left empty stays empty.
	m.focusSide(PaneSeats)
	m.focusZone(ZoneMain)
	plan := m.seatPlan()
	if plan == nil {
		t.Fatal("the coordinates were not used to arrange the map")
	}
	if plan.source != "x/y" || plan.rows != 2 || plan.cols != 3 || plan.compressed {
		t.Fatalf("plan = %+v, want a 3x2 x/y plan with the aisle kept", plan)
	}
	rows := plainLines(panelLines(m, func(slot panelSlot) bool {
		return slot.owner.zone == ZoneMain
	}))
	top := ""
	for _, line := range strings.Split(rows, "\n") {
		if strings.Contains(line, "001") && strings.Contains(line, "002") {
			top = line
			break
		}
	}
	if top == "" {
		t.Fatalf("the coordinate row is not drawn:\n%s", rows)
	}
	// The row is read by cells, not by bytes: the border and the state glyphs are
	// multi-byte, so the slice is taken by display width.
	cell := func(index int) string {
		// +1 skips the panel's left border.
		return ansi.Cut(top, 1+index*seatCellWidth, 1+(index+1)*seatCellWidth)
	}
	if got := cell(0); !strings.Contains(got, "001") {
		t.Errorf("001 is not in the first column of its row: %q\n%s", got, top)
	}
	if got := cell(2); !strings.Contains(got, "002") {
		t.Errorf("002 is not two columns along, reading across the aisle: %q\n%s", got, top)
	}
	if gap := cell(1); strings.TrimSpace(gap) != "" {
		t.Errorf("the aisle column is not empty: %q", gap)
	}

	// Movement follows the map: l crosses the aisle, j goes to the seat below.
	m.room.seats.cursor = 0 // 003, bottom-left in the listing's order
	drain(t, m, feed(t, m, keyMsg('l')), 10)
	if number, _ := m.selectedSeatNumber(); number != "004" {
		t.Fatalf("l from 003 gave %s, want 004 in the same row", number)
	}
	drain(t, m, feed(t, m, keyMsg('k')), 10)
	if number, _ := m.selectedSeatNumber(); number != "002" {
		t.Fatalf("k from 004 gave %s, want 002 in the row above", number)
	}
	drain(t, m, feed(t, m, keyMsg('h')), 10)
	if number, _ := m.selectedSeatNumber(); number != "001" {
		t.Fatalf("h from 002 gave %s, want 001 across the aisle", number)
	}
	// Reading order is the map's, not the listing's: g is the top-left seat of the
	// plan and G the bottom-right one.
	drain(t, m, feed(t, m, keyMsg('g')), 10)
	if number, _ := m.selectedSeatNumber(); number != "001" {
		t.Fatalf("g gave %s, want the first seat in reading order", number)
	}
	drain(t, m, feed(t, m, keyMsg('G')), 10)
	if number, _ := m.selectedSeatNumber(); number != "004" {
		t.Fatalf("G gave %s, want the last seat in reading order", number)
	}

	// The fields are shown where the seats are read: the seat panel's content.
	m.focusSide(PaneSeats)
	view := m.View()
	for _, want := range []string{"网格字段", "坐标字段", "reserveStatus, seatNum, x, y"} {
		if !strings.Contains(view, want) {
			t.Errorf("the seat detail does not report %q:\n%s", want, view)
		}
	}

	// And the observation is durable: the diagnostics log says what the service
	// sent, so it can be answered from a real run without guessing.
	raw, err := os.ReadFile(m.login.Session.Log.Path)
	if err != nil {
		t.Fatalf("the diagnostics log was not written: %v", err)
	}
	for _, want := range []string{`"event":"seatgrid_fields"`, `"fields":"reserveStatus,seatNum,x,y"`, `"coords":"x,y"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the log does not record %s:\n%s", want, raw)
		}
	}
}
