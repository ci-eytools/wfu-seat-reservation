package chaoxing

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Origin is the only host that serves the seat application.
const Origin = "https://office.chaoxing.com"

// Defaults used by the original notebook configuration.
const (
	DefaultFIDEnc = "35bbd135397006a8"
	DefaultMappID = "4109435"
)

// Room is one selectable room from the room list.
type Room struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Capacity   int    `json:"capacity"`
	Selectable bool   `json:"selectable"`
}

// Slot is one selectable time slot for a day.
type Slot struct {
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
}

// HomeInfo is the result of opening the seat landing page.
type HomeInfo struct {
	State        string `json:"state"`
	ServerDay    string `json:"server_day"`
	UsedWfwToken bool   `json:"used_wfw_token"`
}

// RoomOpenInfo describes a room after it has been opened.
type RoomOpenInfo struct {
	Room     string `json:"room"`
	Capacity int    `json:"capacity"`
	Day      string `json:"day"`
	Slots    []Slot `json:"selectable_time_slots"`
}

// SelectionSummary is the public, secret-free view of a selection.
type SelectionSummary struct {
	State             string `json:"state"`
	Day               string `json:"day"`
	RoomID            int    `json:"room_id"`
	Room              string `json:"room"`
	SeatNum           string `json:"seat_num"`
	StartTime         string `json:"start_time"`
	EndTime           string `json:"end_time"`
	SignaturePrepared bool   `json:"signature_prepared"`
	Submitted         bool   `json:"submitted"`
}

// Selection is a locally prepared seat choice. It is signed locally and sent only
// after the user confirms the exact request in the TUI.
type Selection struct {
	Day       string
	RoomID    int
	SeatNum   string
	StartTime string
	EndTime   string
	RoomName  string

	// Submitted records that the service answered a reservation for this
	// selection. It is set from the response, not from the attempt.
	Submitted bool
	// Result is the service's own verdict, kept for the details card.
	Result string

	// payload holds the signed form parameters and is deliberately unexported so
	// it cannot be accidentally logged or rendered.
	payload []pair
}

// Summary returns the display-safe view of the selection.
func (s Selection) Summary() SelectionSummary {
	signature := ""
	for _, p := range s.payload {
		if p.Key == "enc" {
			signature = p.Value
		}
	}
	state := "selected_not_submitted"
	if s.Submitted {
		state = "submitted"
	}
	return SelectionSummary{
		State:             state,
		Day:               s.Day,
		RoomID:            s.RoomID,
		Room:              s.RoomName,
		SeatNum:           s.SeatNum,
		StartTime:         s.StartTime,
		EndTime:           s.EndTime,
		SignaturePrepared: signature != "",
		Submitted:         s.Submitted,
	}
}

// PreparedForm renders the offline form encoding. It never sends a request.
func (s Selection) PreparedForm() string { return encodePairs(s.payload) }

// String redacts the signed payload. It uses a value receiver so that both
// Selection and *Selection satisfy fmt.Stringer, keeping the signature out of
// logs and debug output.
func (s Selection) String() string {
	return "Selection(" + s.Summary().State + " day=" + s.Day + " seat=" + s.SeatNum + ")"
}

// GoString redacts the signed payload under %#v as well.
func (s Selection) GoString() string { return s.String() }

// SeatClient is the read-only seat domain client.
type SeatClient struct {
	Remote     SeatRemote
	remoteView *SeatView
	Login      *QRLogin
	FIDEnc     string
	MappID     string
	Day        string

	// Current is the most recent local selection, if any.
	Current *Selection

	roomID    int
	submitEnc string

	// reserve holds the user's configured write targets, and the page-derived
	// targets are recorded per room by setPageTargets.
	reserve         ReserveConfig
	submitTarget    WriteTarget
	hasSubmitTarget bool

	// windowStatus and windowOpenAt are what the room's reservation window said:
	// whether it is open, and when it opens if it is not yet.
	windowStatus string
	windowOpenAt time.Time

	// signNote explains why nothing can be signed at the moment, when that is the
	// case. An empty note with CanSign false means "no room opened yet".
	signNote  string
	serverNow time.Time
	clockAt   time.Time

	rooms    []roomListItem
	roomData *roomInfoData
	grid     []gridSeat

	// seatMemory is the seat numbering each room has reported. The numbering is
	// room metadata rather than a per-day fact, so remembering it means a day
	// whose room/info reports less can still list every seat.
	seatMemory map[int]SeatLayout

	// gridKeys and gridCoordKeys are the field names the open room's seat listing
	// actually carried. They are observed from the response rather than assumed,
	// which is what makes "does the service describe seat positions?" answerable
	// from a real reply.
	gridKeys      []string
	gridCoordKeys []string
}

// NewSeatClient wires a seat client to an authenticated session.
func NewSeatClient(login *QRLogin, fidEnc, mappID string) *SeatClient {
	if fidEnc == "" {
		fidEnc = DefaultFIDEnc
	}
	if mappID == "" {
		mappID = DefaultMappID
	}
	return &SeatClient{Login: login, FIDEnc: fidEnc, MappID: mappID}
}

// --- remote payload shapes -------------------------------------------------

type roomListItem struct {
	ID              int    `json:"id"`
	FirstLevelName  string `json:"firstLevelName"`
	SecondLevelName string `json:"secondLevelName"`
	ThirdLevelName  string `json:"thirdLevelName"`
	Capacity        int    `json:"capacity"`
	Status          *int   `json:"status"`
	IsShow          *int   `json:"isShow"`
	IsOpen          *int   `json:"isOpen"`
}

type roomListData struct {
	SeatRoomList []roomListItem `json:"seatRoomList"`
	TotalPage    *int           `json:"totalPage"`
}

// serverTime accepts both date strings and Java epoch-millisecond timestamps.
// In particular, 0 is a valid room start (08:00 in Asia/Shanghai), not missing.
type serverTime string

func (s *serverTime) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*s = ""
		return nil
	}
	var text string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &text); err != nil {
			return err
		}
		*s = serverTime(text)
		return nil
	}
	var ms int64
	if err := json.Unmarshal(b, &ms); err != nil {
		return err
	}
	*s = serverTime(time.UnixMilli(ms).In(Shanghai).Format(time.RFC3339Nano))
	return nil
}

type rawSeatRoom struct {
	StartTime       serverTime      `json:"startTime"`
	EndTime         serverTime      `json:"endTime"`
	FirstLevelName  string          `json:"firstLevelName"`
	SecondLevelName string          `json:"secondLevelName"`
	ThirdLevelName  string          `json:"thirdLevelName"`
	Capacity        *int            `json:"capacity"`
	Status          *int            `json:"status"`
	IsOpen          *int            `json:"isOpen"`
	IsShow          *int            `json:"isShow"`
	RoleShow        *bool           `json:"roleShow"`
	PicSeatMode     *int            `json:"picSeatMode"`
	StartSeatNum    *int            `json:"startSeatNum"`
	SeatPauseDate   json.RawMessage `json:"seatPauseDate"`
}

type rawSeatConfig struct {
	TimeUnit           *int    `json:"timeUnit"`
	ReserveMode        *int    `json:"reserveMode"`
	TimeType           *int    `json:"timeType"`
	MinReserveDuration float64 `json:"minReserveDuration"`
	ReserveDuration    float64 `json:"reserveDuration"`
	AllowReserveNow    float64 `json:"allowReserveNow"`
}

type rawInterval struct {
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
}

type rawSeatAttribute struct {
	SeatNum   any   `json:"seatNum"`
	IsReserve *int  `json:"isReserve"`
	LabelIDs  []int `json:"labelIds"`
}

type roomInfoData struct {
	SeatRoom         rawSeatRoom              `json:"seatRoom"`
	SeatConfig       rawSeatConfig            `json:"seatConfig"`
	CommonPauseTimes [][]string               `json:"commonPauseTimes"`
	SeatIntervalMap  map[string][]rawInterval `json:"seatIntervalMap"`
	SeatAttributes   []rawSeatAttribute       `json:"seatAttributes"`
	ServerNow        float64                  `json:"serverNow"`
}

type gridSeat struct {
	SeatNum       any  `json:"seatNum"`
	ReserveStatus *int `json:"reserveStatus"`

	// Some deployments describe the physical layout; these are optional and are
	// used only when they are actually present, so a room without them still
	// gets a grid, just one ordered by seat number.
	Row *int `json:"row,omitempty"`
	Col *int `json:"col,omitempty"`
	X   *int `json:"x,omitempty"`
	Y   *int `json:"y,omitempty"`
}

type pauseEntry struct {
	StartDate any `json:"startDate"`
	EndDate   any `json:"endDate"`
}

// --- helpers ---------------------------------------------------------------

func ptrIntEq(p *int, v int) bool { return p != nil && *p == v }

func ptrBoolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// seatNumString renders a JSON scalar the way Python's str() would.
func seatNumString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "True"
		}
		return "False"
	}
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(toJSON(v)), `"`))
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// zfill3 pads a seat number to three characters, matching str(x).zfill(3).
func zfill3(v any) string {
	s := seatNumString(v)
	if len(s) >= 3 {
		return s
	}
	return strings.Repeat("0", 3-len(s)) + s
}

func containsInt(list []int, want int) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

var isoLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
}

// parseISOFlexible is the counterpart of datetime.fromisoformat followed by
// replacing a missing timezone with Asia/Shanghai. ParseInLocation assigns the
// location only when the input carries no offset, which is exactly that rule.
func parseISOFlexible(s string, loc *time.Location) (time.Time, error) {
	trimmed := strings.TrimSpace(s)
	var lastErr error
	for _, layout := range isoLayouts {
		t, err := time.ParseInLocation(layout, trimmed, loc)
		if err == nil {
			return t, nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

var (
	reServerNow = regexp.MustCompile(`serverNow\s*=\s*new Date\('([^']+)'\)`)
	reInputTag  = regexp.MustCompile(`(?is)<input\b[^>]*>`)
	reAttrID    = regexp.MustCompile(`(?is)(?:^|[\s"'])id\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	reAttrValue = regexp.MustCompile(`(?is)(?:^|[\s"'])value\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
)

func firstGroup(m []string) (string, bool) {
	if m == nil {
		return "", false
	}
	for _, g := range m[1:] {
		if g != "" {
			return html.UnescapeString(g), true
		}
	}
	return "", true
}

// parseSubmitEnc extracts the hidden submit_enc input value, order-independent
// like the original HTMLParser-based implementation.
func parseSubmitEnc(body string) string {
	for _, tag := range reInputTag.FindAllString(body, -1) {
		id, okID := firstGroup(reAttrID.FindStringSubmatch(tag))
		if !okID || id != "submit_enc" {
			continue
		}
		if value, ok := firstGroup(reAttrValue.FindStringSubmatch(tag)); ok {
			return value
		}
	}
	return ""
}

// rawReserveWindow mirrors /room/reserve-window/check. The page's own script uses
// these two fields to decide between "available", "not yet open" and "closed".
type rawReserveWindow struct {
	Status              string `json:"status"`
	BeforeOpenTimeStamp int64  `json:"beforeOpenTimeStamp"`
}

// ReserveWindow reports the room's reservation window: its status token, and the
// moment it opens when the server named one. The time is converted to the local
// zone without modifying the original instant.
func (c *SeatClient) ReserveWindow() (status string, opensAt time.Time, ok bool) {
	if c.windowStatus == "" {
		return "", time.Time{}, false
	}
	if c.windowOpenAt.IsZero() {
		return c.windowStatus, time.Time{}, true
	}
	return c.windowStatus, c.windowOpenAt, true
}

// RoomName mirrors SeatClient.room_name.
func RoomName(r roomListItem) string {
	parts := []string{}
	for _, v := range []string{r.FirstLevelName, r.SecondLevelName, r.ThirdLevelName} {
		if v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " - ")
}

func rawRoomName(r rawSeatRoom) string {
	parts := []string{}
	for _, v := range []string{r.FirstLevelName, r.SecondLevelName, r.ThirdLevelName} {
		if v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " - ")
}

// --- client behaviour ------------------------------------------------------

// jsonCall mirrors SeatClient._json: it performs one read-only query and returns
// the payload's data field.
func (c *SeatClient) jsonCall(ctx context.Context, method, apiPath string, params url.Values) (json.RawMessage, error) {
	var (
		resp *Response
		err  error
	)
	if method == http.MethodGet {
		resp, err = c.Login.Session.Get(ctx, Origin+apiPath, params)
	} else {
		resp, err = c.Login.Session.PostForm(ctx, Origin+apiPath, params)
	}
	if err != nil {
		return nil, opWrap(err, "只读查询失败：%s", apiPath)
	}
	if resp.StatusCode != 200 {
		return nil, opErrf("只读查询失败：%s HTTP %d", apiPath, resp.StatusCode)
	}
	var envelope struct {
		Success *bool           `json:"success"`
		Msg     string          `json:"msg"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &envelope); err != nil {
		return nil, expiredErr("接口未返回 JSON，可能登录已过期")
	}
	if envelope.Success == nil || !*envelope.Success {
		msg := envelope.Msg
		if msg == "" {
			msg = "未知原因"
		}
		return nil, opErr("接口拒绝查询：" + msg)
	}
	return envelope.Data, nil
}

// OpenHome loads the seat landing page and synchronises the server clock.
func (c *SeatClient) OpenHome(ctx context.Context) (*HomeInfo, error) {
	if c.Remote != nil {
		v, e := c.remoteQuery(ctx, "home", "", 0, "", "")
		if e != nil {
			return nil, e
		}
		return v.Home, nil
	}
	homeURL := Origin + "/front/third/apps/seat/index?" + url.Values{
		"fidEnc": {c.FIDEnc},
		"mappId": {c.MappID},
	}.Encode()

	result, err := CheckSeatPage(ctx, c.Login, homeURL, false)
	if err != nil {
		return nil, err
	}
	if result.State != "seat_page_loaded" {
		return nil, expiredErr("未进入已登录座位页：" + describeSeatPage(result))
	}
	if c.Login.SeatPageResponse == nil {
		return nil, opErr("座位首页响应丢失")
	}
	m := reServerNow.FindStringSubmatch(c.Login.SeatPageResponse.Text())
	if m == nil {
		return nil, opErr("未找到服务器时间")
	}
	serverNow, err := parseISOFlexible(m[1], Shanghai)
	if err != nil {
		return nil, opErr("服务器时间格式无法解析：" + m[1])
	}
	c.serverNow = serverNow
	c.clockAt = time.Now()
	c.Day = serverNow.In(Shanghai).Format("2006-01-02")
	return &HomeInfo{State: "seat_home_loaded", ServerDay: c.Day, UsedWfwToken: false}, nil
}

func describeSeatPage(r *SeatPageResult) string {
	if r == nil {
		return "无响应"
	}
	title := ""
	if r.Title != nil {
		title = *r.Title
	}
	return "state=" + r.State + " status=" + strconv.Itoa(r.Status) + " title=" + title
}

// ServerNow returns the synchronised server time.
func (c *SeatClient) ServerNow() (time.Time, error) {
	if c.serverNow.IsZero() {
		return time.Time{}, inputErr("请先打开首页")
	}
	return c.serverNow.Add(time.Since(c.clockAt)), nil
}

// ListRooms loads the room list for the day, paging like the original.
func (c *SeatClient) ListRooms(ctx context.Context, day string) ([]Room, error) {
	if c.Remote != nil {
		v, e := c.remoteQuery(ctx, "rooms", day, 0, "", "")
		if e != nil {
			return nil, e
		}
		return v.Rooms, nil
	}
	if day != "" {
		c.Day = day
	}
	if c.Day == "" {
		return nil, inputErr("请先打开首页")
	}
	if _, err := time.Parse("2006-01-02", c.Day); err != nil {
		return nil, inputErr("日期必须是 YYYY-MM-DD 格式")
	}

	all := []roomListItem{}
	completed := false
	for page := 1; page <= 20; page++ {
		raw, err := c.jsonCall(ctx, http.MethodGet, "/data/apps/seat/room/list", url.Values{
			"time":            {""},
			"cpage":           {strconv.Itoa(page)},
			"pageSize":        {"100"},
			"firstLevelName":  {""},
			"secondLevelName": {""},
			"thirdLevelName":  {""},
			"day":             {c.Day},
			"deptIdEnc":       {c.FIDEnc},
		})
		if err != nil {
			return nil, err
		}
		var payload roomListData
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, opErr("房间列表结构无法解析")
		}
		all = append(all, payload.SeatRoomList...)
		total := 1
		if payload.TotalPage != nil {
			total = *payload.TotalPage
		}
		if page >= total {
			completed = true
			break
		}
	}
	if !completed {
		return nil, opErr("房间分页超出预期")
	}
	c.rooms = all

	out := make([]Room, 0, len(all))
	for _, r := range all {
		out = append(out, Room{
			ID:         r.ID,
			Name:       RoomName(r),
			Capacity:   r.Capacity,
			Selectable: ptrIntEq(r.Status, 0) && ptrIntEq(r.IsShow, 0) && !ptrIntEq(r.IsOpen, 1),
		})
	}
	return out, nil
}

// OpenRoom validates and loads one room, including its seat grid and slots.
func (c *SeatClient) OpenRoom(ctx context.Context, roomID int) (*RoomOpenInfo, error) {
	if c.Remote != nil {
		v, e := c.remoteQuery(ctx, "room", c.Day, roomID, "", "")
		if e != nil {
			return nil, e
		}
		return v.Room, nil
	}
	c.setPageTargets(pageTargets{})
	c.Current = nil
	c.roomData = nil
	c.submitEnc = ""
	c.grid = nil
	c.gridKeys = nil
	c.gridCoordKeys = nil
	c.roomID = 0

	known := false
	for _, r := range c.rooms {
		if r.ID == roomID {
			known = true
			break
		}
	}
	if !known {
		return nil, inputErr("请从当前房间列表选择ID")
	}

	windowRaw, err := c.jsonCall(ctx, http.MethodGet, "/data/apps/seat/room/reserve-window/check", url.Values{
		"roomId":    {strconv.Itoa(roomID)},
		"day":       {c.Day},
		"deptIdEnc": {c.FIDEnc},
		"fidEnc":    {c.FIDEnc},
	})
	if err != nil {
		return nil, err
	}
	var window rawReserveWindow
	if err := json.Unmarshal(windowRaw, &window); err != nil {
		return nil, opErr("预约窗口响应无法解析")
	}
	c.windowStatus = strings.ToUpper(strings.TrimSpace(window.Status))
	c.windowOpenAt = time.Time{}
	if window.BeforeOpenTimeStamp > 0 {
		c.windowOpenAt = time.UnixMilli(window.BeforeOpenTimeStamp).In(Shanghai)
	}
	// BEFORE_OPEN is exactly when planning ahead matters: the room is still
	// readable, and the opening moment is what a scheduled request aims at. Any
	// other status is a hard stop, because there is nothing to plan for.
	if c.windowStatus != "AVAILABLE" && c.windowStatus != "BEFORE_OPEN" {
		return nil, inputErr("该日期预约窗口不可用：" + window.Status)
	}

	infoRaw, err := c.jsonCall(ctx, http.MethodPost, "/data/apps/seat/room/info", url.Values{
		"id":           {strconv.Itoa(roomID)},
		"toDay":        {c.Day},
		"fidEnc":       {c.FIDEnc},
		"queryReserve": {"true"},
	})
	if err != nil {
		return nil, err
	}
	var info roomInfoData
	if err := json.Unmarshal(infoRaw, &info); err != nil {
		return nil, opErr("房间详情结构无法解析")
	}
	room := info.SeatRoom
	if !ptrIntEq(room.Status, 0) || ptrIntEq(room.IsOpen, 1) || ptrIntEq(room.IsShow, 1) {
		return nil, inputErr("房间当前不可在线预约")
	}
	if !ptrBoolOr(room.RoleShow, true) {
		return nil, inputErr("当前身份不能选择此房间")
	}

	c.roomID = roomID
	c.roomData = &info

	// The select page is where the submit token and the form target come from, and
	// a day whose window has not opened yet simply does not serve it: it answers
	// with a redirect or an error instead of a form. That is not a reason to hide
	// the room -- its periods and its whole seat numbering came from room/info --
	// so this is best effort, and what failed is recorded for the panels to say.
	c.submitEnc = ""
	c.signNote = ""
	if selectResp, err := c.Login.Session.Get(ctx, Origin+"/front/third/apps/seat/select", url.Values{
		"deptIdEnc": {c.FIDEnc},
		"id":        {strconv.Itoa(roomID)},
		"day":       {c.Day},
		"backLevel": {"2"},
		"fidEnc":    {c.FIDEnc},
	}); err != nil {
		c.signNote = "选座页请求失败：" + err.Error()
	} else if selectResp.StatusCode != 200 {
		c.signNote = fmt.Sprintf("选座页返回 HTTP %d（预约窗口未开放时服务端不提供）", selectResp.StatusCode)
	} else if page := selectResp.Text(); parseSubmitEnc(page) != "" {
		c.submitEnc = parseSubmitEnc(page)
		// The page itself is the only authority on where its form goes; the
		// targets are recorded but nothing is sent until the user confirms.
		targets := parsePageTargets(page)
		if !targets.hasSub {
			targets = c.loadScriptTarget(ctx, page, targets)
		}
		c.setPageTargets(targets)
	} else {
		c.signNote = "选座页没有提供提交编码（预约窗口未开放）"
	}
	c.serverNow = time.UnixMilli(int64(info.ServerNow)).In(Shanghai)
	c.clockAt = time.Now()

	switch {
	case ptrIntEq(room.PicSeatMode, 2):
		gridRaw, err := c.jsonCall(ctx, http.MethodGet, "/data/apps/seat/seatgrid/roomid", url.Values{
			"roomId": {strconv.Itoa(roomID)},
			"fidEnc": {c.FIDEnc},
		})
		if err != nil {
			return nil, err
		}
		var grid struct {
			SeatDatas []gridSeat `json:"seatDatas"`
		}
		if err := json.Unmarshal(gridRaw, &grid); err != nil {
			return nil, opErr("座位网格结构无法解析")
		}
		c.grid = grid.SeatDatas
		c.observeGridKeys(gridRaw)
	case ptrIntEq(room.PicSeatMode, 0):
		return nil, unsupportedErr("当前版本支持网格/列表座位；自由布局需另外适配，未猜测座位数据")
	default:
		// List mode. The seat numbering is room metadata, but an unopened day may
		// report less of it than an open one; what the room has already told us,
		// and what the day's own room list says about its capacity, keep the whole
		// seat range listable instead of blanking the room.
		start, total := c.layoutRange(roomID, room)
		zero := 0
		for n := start; n < start+total; n++ {
			c.grid = append(c.grid, gridSeat{SeatNum: n, ReserveStatus: &zero})
		}
	}

	// Remember this room's numbering, so a later day that says less about it can
	// still show the same seats.
	layout, _ := c.SeatLayout()

	// The listing's own fields are worth a durable line: whether a room's seats
	// come with positions is a property of the service's data, and this records
	// what it actually sent rather than what was expected.
	if len(c.gridKeys) > 0 && c.Login != nil && c.Login.Session != nil && c.Login.Session.Log != nil {
		c.Login.Session.Log.Event("seatgrid_fields", map[string]any{
			"room_id": roomID,
			"seats":   len(c.grid),
			"fields":  strings.Join(c.gridKeys, ","),
			"coords":  strings.Join(c.gridCoordKeys, ","),
		})
	}

	slots, err := c.TimeSlots()
	if err != nil {
		return nil, err
	}
	return &RoomOpenInfo{
		Room:     rawRoomName(room),
		Capacity: layout.Total,
		Day:      c.Day,
		Slots:    slots,
	}, nil
}

func parsePauseDates(raw json.RawMessage) ([]pauseEntry, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var container struct {
		DateJSON string `json:"dateJson"`
	}
	if err := json.Unmarshal(raw, &container); err != nil {
		return nil, opErr("暂停日期结构无法解析")
	}
	if container.DateJSON == "" {
		return nil, nil
	}
	var entries []pauseEntry
	if err := json.Unmarshal([]byte(container.DateJSON), &entries); err != nil {
		return nil, opErr("暂停日期列表无法解析")
	}
	return entries, nil
}

func (c *SeatClient) parseTimeValue(v any) (time.Time, error) {
	switch t := v.(type) {
	case float64:
		return time.UnixMilli(int64(t)).In(Shanghai), nil
	case string:
		parsed, err := parseISOFlexible(t, Shanghai)
		if err != nil {
			return time.Time{}, opErr("暂停时间无法解析：" + t)
		}
		return parsed, nil
	}
	return time.Time{}, opErr("暂停时间格式未知")
}

// TimeSlots returns the still-selectable slots for the opened room.
func (c *SeatClient) TimeSlots() ([]Slot, error) {
	if c.roomData == nil {
		return nil, inputErr("请先打开房间")
	}
	cfg := c.roomData.SeatConfig
	room := c.roomData.SeatRoom

	if !ptrIntEq(cfg.ReserveMode, 0) {
		return nil, unsupportedErr("当前实现支持普通时段预约模式")
	}
	if !ptrIntEq(cfg.TimeType, 1) && !(ptrIntEq(cfg.TimeType, 0) && cfg.TimeUnit != nil) {
		return nil, unsupportedErr("当前实现使用服务器配置时段；连续时间模式尚未适配")
	}

	dayTime, err := time.ParseInLocation("2006-01-02", c.Day, Shanghai)
	if err != nil {
		return nil, inputErr("日期必须是 YYYY-MM-DD 格式")
	}
	weekday := int(dayTime.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	raw := c.roomData.SeatIntervalMap[strconv.Itoa(weekday)]
	if ptrIntEq(cfg.TimeType, 0) {
		start, e := parseISOFlexible(string(room.StartTime), Shanghai)
		if e != nil {
			return nil, opErr("开放开始时间无法解析")
		}
		end, e := parseISOFlexible(string(room.EndTime), Shanghai)
		if e != nil {
			return nil, opErr("开放结束时间无法解析")
		}
		unit := *cfg.TimeUnit
		if unit < 0 || unit > 1440 || !end.After(start) || end.Sub(start) > 24*time.Hour {
			return nil, opErr("服务器时间粒度或开放区间无效")
		}
		raw = nil
		for at := start; at.Before(end); {
			next := end
			if unit > 0 {
				next = at.Add(time.Duration(unit) * time.Minute)
				if next.After(end) {
					next = end
				}
			}
			raw = append(raw, rawInterval{StartTime: at.Format("15:04"), EndTime: next.Format("15:04")})
			at = next
		}
	}

	pause, err := parsePauseDates(room.SeatPauseDate)
	if err != nil {
		return nil, err
	}
	now, err := c.ServerNow()
	if err != nil {
		return nil, err
	}
	lead := 0.0
	if cfg.AllowReserveNow > 1 {
		lead = math.Max(0, cfg.AllowReserveNow)
	}
	cutoff := now.Add(time.Duration(lead * float64(time.Millisecond)))

	slots := []Slot{}
	for _, entry := range raw {
		start, err := parseISOFlexible(c.Day+"T"+entry.StartTime, Shanghai)
		if err != nil {
			return nil, opErr("时段开始时间无法解析：" + entry.StartTime)
		}
		end, err := parseISOFlexible(c.Day+"T"+entry.EndTime, Shanghai)
		if err != nil {
			return nil, opErr("时段结束时间无法解析：" + entry.EndTime)
		}
		if !start.After(cutoff) {
			continue
		}
		skipped := false
		for _, pair := range c.roomData.CommonPauseTimes {
			if len(pair) != 2 {
				return nil, opErr("暂停时段格式无效")
			}
			a, e := parseISOFlexible(pair[0], Shanghai)
			if e != nil {
				return nil, e
			}
			b, e := parseISOFlexible(pair[1], Shanghai)
			if e != nil {
				return nil, e
			}
			if start.Before(b) && end.After(a) {
				skipped = true
			}
		}
		for _, p := range pause {
			pStart, err := c.parseTimeValue(p.StartDate)
			if err != nil {
				return nil, err
			}
			pEnd, err := c.parseTimeValue(p.EndDate)
			if err != nil {
				return nil, err
			}
			if start.Before(pEnd) && end.After(pStart) {
				skipped = true
				break
			}
		}
		if skipped {
			continue
		}
		slots = append(slots, Slot{StartTime: entry.StartTime, EndTime: entry.EndTime})
	}
	return slots, nil
}

// validatePeriod checks that start..end is a contiguous run of open slots.
func (c *SeatClient) validatePeriod(start, end string) error {
	slots, err := c.TimeSlots()
	if err != nil {
		return err
	}
	cursor := start
	for _, slot := range slots {
		if slot.StartTime == cursor {
			cursor = slot.EndTime
			if cursor == end {
				break
			}
		}
	}
	if cursor != end || start >= end {
		return inputErr("所选时段已过期、不开放或跨越暂停时段")
	}
	cfg := c.roomData.SeatConfig
	startAt, err := time.ParseInLocation("15:04", start, Shanghai)
	if err != nil {
		return inputErr("开始时间必须是 HH:MM 格式")
	}
	endAt, err := time.ParseInLocation("15:04", end, Shanghai)
	if err != nil {
		return inputErr("结束时间必须是 HH:MM 格式")
	}
	hours := endAt.Sub(startAt).Hours()
	if hours < cfg.MinReserveDuration {
		return inputErr("少于最低预约时长")
	}
	if cfg.ReserveDuration > 0 && hours > cfg.ReserveDuration {
		return inputErr("超过最高预约时长")
	}
	return nil
}

// SeatStatus is one seat's state for a single period.
type SeatStatus int

const (
	// SeatFree is a seat that can be reserved for the period.
	SeatFree SeatStatus = iota
	// SeatOccupied is already taken for the period.
	SeatOccupied
	// SeatDisabled is administratively unavailable (seatAttributes says so).
	SeatDisabled
	// SeatUnavailable is marked unusable by the room's own seat map.
	SeatUnavailable
)

func (s SeatStatus) String() string {
	switch s {
	case SeatFree:
		return "空闲"
	case SeatOccupied:
		return "已占用"
	case SeatDisabled:
		return "不可选"
	case SeatUnavailable:
		return "不可用"
	}
	return "未知"
}

// SeatState is one seat and its state for a period.
type SeatState struct {
	Num    string
	Status SeatStatus
}

// Occupancy is the complete seat picture for one period. It keeps the taken
// seats, unlike the free-seat list, so the UI can mark them.
type Occupancy struct {
	Start, End string
	Seats      []SeatState

	Free        int
	Occupied    int
	Disabled    int
	Unavailable int
}

// Total is the number of seats the room reports.
func (o *Occupancy) Total() int { return len(o.Seats) }

// SeatMap returns every seat in the opened room with its state for the period.
// It issues exactly one read-only query, the same one AvailableSeats uses.
func (c *SeatClient) SeatMap(ctx context.Context, start, end string) (*Occupancy, error) {
	if c.Remote != nil {
		v, e := c.Remote.Query(ctx, SeatQuery{"seats", c.Day, c.roomID, start, end})
		if e != nil {
			return nil, e
		}
		return v.Occupancy, nil
	}
	if err := c.validatePeriod(start, end); err != nil {
		return nil, err
	}
	occupied, err := c.fetchOccupied(ctx, start, end)
	if err != nil {
		return nil, err
	}
	disabled := c.disabledSeats()

	out := &Occupancy{Start: start, End: end, Seats: make([]SeatState, 0, len(c.grid))}
	seen := map[string]bool{}
	for _, seat := range c.grid {
		num := zfill3(seat.SeatNum)
		if num == "" || seen[num] {
			continue
		}
		seen[num] = true

		status := SeatFree
		switch {
		case disabled[num]:
			status = SeatDisabled
		case occupied[num]:
			status = SeatOccupied
		case !ptrIntEq(seat.ReserveStatus, 0):
			status = SeatUnavailable
		}
		out.Seats = append(out.Seats, SeatState{Num: num, Status: status})
		switch status {
		case SeatFree:
			out.Free++
		case SeatOccupied:
			out.Occupied++
		case SeatDisabled:
			out.Disabled++
		case SeatUnavailable:
			out.Unavailable++
		}
	}
	sort.Slice(out.Seats, func(i, j int) bool { return out.Seats[i].Num < out.Seats[j].Num })
	return out, nil
}

// fetchOccupied returns the seats the service reports as taken for the period.
func (c *SeatClient) fetchOccupied(ctx context.Context, start, end string) (map[string]bool, error) {
	raw, err := c.jsonCall(ctx, http.MethodPost, "/data/apps/seat/getusedseatnums", url.Values{
		"roomId":    {strconv.Itoa(c.roomID)},
		"startTime": {start},
		"endTime":   {end},
		"day":       {c.Day},
		"fidEnc":    {c.FIDEnc},
	})
	if err != nil {
		return nil, err
	}
	var payload struct {
		SeatReserves []struct {
			SeatNum any `json:"seatNum"`
		} `json:"seatReserves"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, opErr("占用座位结构无法解析")
	}
	occupied := make(map[string]bool, len(payload.SeatReserves))
	for _, reserve := range payload.SeatReserves {
		occupied[zfill3(reserve.SeatNum)] = true
	}
	return occupied, nil
}

// disabledSeats returns the seats the room marks as not reservable.
func (c *SeatClient) disabledSeats() map[string]bool {
	disabled := map[string]bool{}
	for _, attribute := range c.roomData.SeatAttributes {
		if ptrIntEq(attribute.IsReserve, 0) || containsInt(attribute.LabelIDs, 5) {
			disabled[zfill3(attribute.SeatNum)] = true
		}
	}
	return disabled
}

// AvailableSeats returns the free seat numbers for the period. It is the free
// subset of SeatMap, so both views of the same period always agree.
func (c *SeatClient) AvailableSeats(ctx context.Context, start, end string) ([]string, error) {
	occupancy, err := c.SeatMap(ctx, start, end)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, occupancy.Free)
	for _, seat := range occupancy.Seats {
		if seat.Status == SeatFree {
			out = append(out, seat.Num)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Choose prepares a local selection. It re-queries availability first, exactly
// like the original, so a stale UI cannot select an occupied seat.
func (c *SeatClient) Choose(ctx context.Context, seatNum, start, end string) (*Selection, error) {
	seat := zfill3(seatNum)
	available, err := c.AvailableSeats(ctx, start, end)
	if err != nil {
		return nil, err
	}
	if !containsString(available, seat) {
		return nil, inputErr("所选座位在该时段不可用")
	}
	return c.prepareSelection(seat, start, end)
}

// Plan prepares a selection without re-querying availability. It exists for
// planning ahead: a seat that is occupied now may be free when the request is
// meant to fire, and the point of a scheduled request is to choose it before
// then. No network I/O happens here -- the signature is local -- so the caller
// has to be honest about the seat's current state on screen.
func (c *SeatClient) Plan(seatNum, start, end string) (*Selection, error) {
	if c.Remote != nil {
		c.Current = &Selection{Day: c.Day, RoomID: c.roomID, SeatNum: seatNum, StartTime: start, EndTime: end}
		return c.Current, nil
	}
	return c.prepareSelection(zfill3(seatNum), start, end)
}

// CanSign reports whether the seat page has handed over a submit token. A day
// whose window has not opened yet serves the page without one, and a seat chosen
// for such a day is a pre-order rather than a signed reservation.
func (c *SeatClient) CanSign() bool { return (c.Remote != nil && c.roomID > 0) || c.submitEnc != "" }

// SignNote explains why signing is unavailable, in the words of what happened.
func (c *SeatClient) SignNote() string { return c.signNote }

// prepareSelection signs the page's parameter set into a local selection.
func (c *SeatClient) prepareSelection(seat, start, end string) (*Selection, error) {
	if c.submitEnc == "" {
		return nil, unsupportedErr("选座页尚未提供提交编码（预约窗口未开放）· 该座位可以先预订")
	}

	base := []pair{
		{Key: "deptIdEnc", Value: c.FIDEnc},
		{Key: "roomId", Value: strconv.Itoa(c.roomID)},
		{Key: "day", Value: c.Day},
		{Key: "startTime", Value: start},
		{Key: "endTime", Value: end},
		{Key: "seatNum", Value: seat},
		{Key: "captcha", Value: ""},
		{Key: "wyToken", Value: ""},
	}
	payload := append([]pair{}, base...)
	payload = append(payload, pair{Key: "enc", Value: signPayload(base, c.submitEnc)})

	c.Current = &Selection{
		Day:       c.Day,
		RoomID:    c.roomID,
		SeatNum:   seat,
		StartTime: start,
		EndTime:   end,
		RoomName:  rawRoomName(c.roomData.SeatRoom),
		payload:   payload,
	}
	return c.Current, nil
}

// signPayload is the page's own signature: the parameters in sorted-key order,
// each rendered as "[k=v]", followed by the page's submit_enc token, hashed with
// MD5. It is shared by the seat reservation and the waitlist request so both
// cover exactly the parameters they send.
func signPayload(base []pair, submitEnc string) string {
	keys := make([]string, 0, len(base))
	lookup := map[string]string{}
	for _, p := range base {
		keys = append(keys, p.Key)
		lookup[p.Key] = p.Value
	}
	sort.Strings(keys)
	var signing strings.Builder
	for _, k := range keys {
		signing.WriteString("[" + k + "=" + lookup[k] + "]")
	}
	signing.WriteString("[" + submitEnc + "]")
	sum := md5.Sum([]byte(signing.String()))
	return hex.EncodeToString(sum[:])
}

// Submit is the Python API's always-refusing entry point, kept so an existing
// caller cannot silently gain the ability to write. Real submission needs a
// context, an armed target and a confirmation; it is SubmitReservation.
func (c *SeatClient) Submit() error {
	return &BlockedSeatRequest{
		Reason: "提交需要明确的接口目标与用户确认，请使用 SubmitReservation",
	}
}

// SeatLayout describes how the room arranges its seats and which numbers it uses.
// It is room metadata, not occupancy: it is what the seat list shows before
// anything is queried.
type SeatLayout struct {
	Mode string
	// Start is the room's first seat number, Total how many it has.
	Start int
	Total int
	// Grid is how many seats the room's grid listing holds, 0 when the room is a
	// plain list.
	Grid int
}

// SeatSpot is where one seat is, using only what the service reports: the room's
// own numbering, its position in the grid listing when there is one, and its zone
// labels. No coordinates are invented when the service does not give any.
type SeatSpot struct {
	Number string
	// Ordinal is the seat's 1-based position in the room's numbering.
	Ordinal int
	// GridOrdinal is its 1-based position in the grid listing, 0 when unknown.
	GridOrdinal int
	GridTotal   int
	Labels      []int
	// Paused reports the room's own "this seat cannot be reserved" flag.
	Paused bool
	// Col, Row and CoordSource are the service's own position when it reported one.
	Col, Row    int
	CoordSource string
}

// SeatCell is one seat in the room's grid listing, with the position the service
// itself reports. Both x/y and row/col are read; x is the column and y the row, and
// Source names the pair the position came from so a panel can say which.
type SeatCell struct {
	Number string
	// Col and Row are 1-based when HasCoords is true.
	Col, Row  int
	HasCoords bool
	// Source is "x/y" or "row/col".
	Source string
}

// SeatGrid is the open room's seat listing in the order the service gives it,
// which is the order the seats are laid out in.
func (c *SeatClient) SeatGrid() []SeatCell {
	out := make([]SeatCell, 0, len(c.grid))
	for _, seat := range c.grid {
		cell := SeatCell{Number: zfill3(seatNumString(seat.SeatNum))}
		if col, row, source := seatCoords(seat); source != "" {
			cell.Col, cell.Row, cell.HasCoords, cell.Source = col, row, true, source
		}
		out = append(out, cell)
	}
	return out
}

// seatCoords reads a listing entry's position. x/y wins over row/col when both are
// present: they are the service's own picture coordinates.
func seatCoords(seat gridSeat) (col, row int, source string) {
	switch {
	case seat.X != nil && seat.Y != nil:
		return *seat.X, *seat.Y, "x/y"
	case seat.Col != nil && seat.Row != nil:
		return *seat.Col, *seat.Row, "row/col"
	}
	return 0, 0, ""
}

// SeatCoords reports the position the service gave for every listed seat, and which
// pair of fields it came from. ok is false for a list-mode room, for a listing that
// leaves any seat without a position, and for one that mixes the two field pairs:
// a plan built from half the data would be a guess, and this tool does not guess.
func (c *SeatClient) SeatCoords() (map[string][2]int, string, bool) {
	if len(c.grid) == 0 {
		return nil, "", false
	}
	out := make(map[string][2]int, len(c.grid))
	source := ""
	for _, seat := range c.grid {
		col, row, from := seatCoords(seat)
		if from == "" || col <= 0 || row <= 0 {
			return nil, "", false
		}
		if source == "" {
			source = from
		} else if source != from {
			return nil, "", false
		}
		out[zfill3(seatNumString(seat.SeatNum))] = [2]int{col, row}
	}
	if len(out) == 0 {
		return nil, "", false
	}
	return out, source, true
}

// GridWidth is how many seats the room's own layout puts on one row, when the
// service reports coordinates. Zero means "not reported", and the caller falls
// back to flowing the seats by number.
func (c *SeatClient) GridWidth() int {
	widest := 0
	for _, seat := range c.grid {
		if col, _, source := seatCoords(seat); source != "" && col > widest {
			widest = col
		}
	}
	return widest
}

// layoutRange resolves a room's seat numbering. The current room/info is the
// authority; when it does not carry the range -- which an unopened day's response
// may not -- the numbering remembered for the room and the day's own room list are
// used instead, in that order. The numbering is room metadata, so a day that
// reports less must not cost the user the room's seats.
func (c *SeatClient) layoutRange(roomID int, room rawSeatRoom) (start, total int) {
	if room.StartSeatNum != nil {
		start = *room.StartSeatNum
	}
	if room.Capacity != nil {
		total = *room.Capacity
	}
	if start <= 0 || total <= 0 {
		if remembered, ok := c.rememberedLayout(roomID); ok {
			if start <= 0 {
				start = remembered.Start
			}
			if total <= 0 {
				total = remembered.Total
			}
		}
	}
	if total <= 0 {
		for _, entry := range c.rooms {
			if entry.ID == roomID && entry.Capacity > 0 {
				total = entry.Capacity
				break
			}
		}
	}
	if start <= 0 {
		start = 1
	}
	if total < 0 {
		total = 0
	}
	return start, total
}

// rememberLayout keeps a room's seat numbering for later days to reuse.
func (c *SeatClient) rememberLayout(roomID int, layout SeatLayout) {
	if roomID == 0 || layout.Total <= 0 {
		return
	}
	if c.seatMemory == nil {
		c.seatMemory = map[int]SeatLayout{}
	}
	c.seatMemory[roomID] = layout
}

// rememberedLayout reports the room's last known seat numbering, if any.
func (c *SeatClient) rememberedLayout(roomID int) (SeatLayout, bool) {
	layout, ok := c.seatMemory[roomID]
	return layout, ok && layout.Total > 0
}

// coordinateKeyNames are the field names that would describe where a seat physically
// is. They are reported to the panels, never interpreted: the map is laid out by
// seat number, so a room whose listing carries coordinates loses nothing and
// implies nothing.
var coordinateKeyNames = map[string]bool{
	"row": true, "col": true, "x": true, "y": true,
	"seatx": true, "seaty": true, "left": true, "top": true,
}

// observeGridKeys records the field names the seat listing carried, from the
// response itself.
func (c *SeatClient) observeGridKeys(raw json.RawMessage) {
	c.gridKeys, c.gridCoordKeys = nil, nil
	var container map[string]json.RawMessage
	if err := json.Unmarshal(raw, &container); err != nil {
		return
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(container["seatDatas"], &entries); err != nil {
		return
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		for key := range entry {
			if seen[key] {
				continue
			}
			seen[key] = true
			c.gridKeys = append(c.gridKeys, key)
			if coordinateKeyNames[strings.ToLower(key)] {
				c.gridCoordKeys = append(c.gridCoordKeys, key)
			}
		}
	}
	sort.Strings(c.gridKeys)
	sort.Strings(c.gridCoordKeys)
}

// SeatOrder is the open room's seats in the order the service listed them, when it
// sent a listing at all. A room in grid mode (picSeatMode 2) draws its seats from
// this listing, so it -- not the numbering range -- is authoritative for that room;
// a list-mode room has no listing and returns nil.
func (c *SeatClient) SeatOrder() []string {
	if c.Remote != nil {
		if c.remoteView == nil {
			return nil
		}
		return append([]string(nil), c.remoteView.Order...)
	}
	// Only a grid room is *listed* by the service. A list-mode room's grid is
	// synthesised from startSeatNum and capacity, which the numbering range already
	// describes, so it is not treated as a listing.
	if c.roomData == nil || len(c.grid) == 0 || !ptrIntEq(c.roomData.SeatRoom.PicSeatMode, 2) {
		return nil
	}
	out := make([]string, 0, len(c.grid))
	for _, seat := range c.grid {
		number := zfill3(seatNumString(seat.SeatNum))
		if number == "" || number == "000" {
			continue
		}
		out = append(out, number)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// GridKeys reports the field names the open room's seat listing carried, sorted.
// It is empty for a list-mode room, whose numbering is synthesised from
// startSeatNum and capacity rather than listed by the service.
func (c *SeatClient) GridKeys() []string {
	return append([]string(nil), c.gridKeys...)
}

// GridCoordKeys reports which of the listing's fields look like a seat's physical
// position. The map does not use them yet; they are shown so the service's data is
// visible instead of guessed at.
func (c *SeatClient) GridCoordKeys() []string {
	return append([]string(nil), c.gridCoordKeys...)
}

// SeatLayout reports the open room's seat numbering. It is complete whenever the
// room has ever reported a range: the day's own numbers win, and what the room
// said before is used when this day's response is thinner.
func (c *SeatClient) SeatLayout() (SeatLayout, bool) {
	if c.Remote != nil {
		if c.remoteView == nil {
			return SeatLayout{}, false
		}
		return c.remoteView.Layout, c.remoteView.HasLayout
	}
	if c.roomData == nil {
		return SeatLayout{}, false
	}
	room := c.roomData.SeatRoom
	mode := "列表模式"
	if ptrIntEq(room.PicSeatMode, 2) {
		mode = "网格模式"
	}
	start, total := c.layoutRange(c.roomID, room)
	if len(c.grid) > 0 && total <= 0 {
		// The listing itself is the numbering when nothing else says how long it is.
		total = len(c.grid)
	}
	if total > 0 && len(c.grid) == 0 && !ptrIntEq(room.PicSeatMode, 2) {
		// The numbering is known but the listing was not built for this day, so
		// the seat range is what the room's own numbers describe.
		for n := start; n < start+total; n++ {
			c.grid = append(c.grid, gridSeat{SeatNum: n})
		}
	}
	layout := SeatLayout{Mode: mode, Start: start, Total: total, Grid: len(c.grid)}
	c.rememberLayout(c.roomID, layout)
	return layout, true
}

// SeatNumber renders the nth seat of the open room the way the service does.
func (c *SeatClient) SeatNumber(index int) (string, bool) {
	layout, ok := c.SeatLayout()
	if !ok || index < 0 || index >= layout.Total {
		return "", false
	}
	return zfill3(layout.Start + index), true
}

// SeatSpot reports where a seat is, from the room's own data.
func (c *SeatClient) SeatSpot(seatNum string) (SeatSpot, bool) {
	if c.Remote != nil {
		if c.remoteView == nil {
			return SeatSpot{}, false
		}
		spot, ok := c.remoteView.Spots[zfill3(seatNum)]
		return spot, ok
	}
	layout, ok := c.SeatLayout()
	if !ok {
		return SeatSpot{}, false
	}
	number := zfill3(seatNum)
	value, err := strconv.Atoi(number)
	if err != nil {
		return SeatSpot{}, false
	}
	spot := SeatSpot{
		Number:  number,
		Ordinal: value - layout.Start + 1,
	}
	if spot.Ordinal < 1 || spot.Ordinal > layout.Total {
		return SeatSpot{}, false
	}
	for i, seat := range c.grid {
		if zfill3(seatNumString(seat.SeatNum)) != number {
			continue
		}
		spot.GridOrdinal = i + 1
		if col, row, source := seatCoords(seat); source != "" {
			spot.Col, spot.Row, spot.CoordSource = col, row, source
		}
		break
	}
	spot.GridTotal = layout.Grid
	for _, attribute := range c.roomData.SeatAttributes {
		if zfill3(seatNumString(attribute.SeatNum)) != number {
			continue
		}
		spot.Labels = append([]int(nil), attribute.LabelIDs...)
		spot.Paused = ptrIntEq(attribute.IsReserve, 0)
		break
	}
	return spot, true
}
