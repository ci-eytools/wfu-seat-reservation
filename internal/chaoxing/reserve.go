package chaoxing

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Reservation targets come from either the select form or the referenced
// official select script. Script targets require both the submit call and its
// POST helper definition; discovering a target never sends a reservation.

// WriteTarget is a mutating request the seat page (or the user) named.
type WriteTarget struct {
	Method string
	URL    string
	// Source says where the target came from so the confirmation can be honest
	// about it: "选座页表单", "选座页脚本" or "设置".
	Source string
	// Note carries the page's own vocabulary for the target, when it had any.
	Note string
}

// Path is the target's path, for display and for arming.
func (t WriteTarget) Path() string {
	u, err := url.Parse(t.URL)
	if err != nil {
		return t.URL
	}
	return u.Path
}

var (
	reFormTag = regexp.MustCompile(`(?is)<form\b[^>]*>`)
	// One group per quote style, so the first non-empty group is the value itself.
	reAttrAny = regexp.MustCompile(`(?is)\baction\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	reMethod  = regexp.MustCompile(`(?is)\bmethod\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	reFormEnd = regexp.MustCompile(`(?is)</form>`)
	reURLInJS = regexp.MustCompile(`["'\x60](/data/apps/seat/[A-Za-z0-9/_.\-]*)["'\x60]`)
)

// parseSubmitTarget reads the action of the form that carries submit_enc. If no
// form carries it, the first form on the page is used, because that is still the
// page's own statement of where its data goes.
func parseSubmitTarget(body string) (WriteTarget, bool) {
	form := formContaining(body, "submit_enc")
	if form == "" {
		if loc := reFormTag.FindStringIndex(body); loc != nil {
			form = formAt(body, loc)
		}
	}
	if form == "" {
		return WriteTarget{}, false
	}
	match := reAttrAny.FindStringSubmatch(form)
	action, ok := firstGroup(match)
	action = strings.TrimSpace(action)
	if !ok || action == "" || strings.HasPrefix(action, "#") {
		return WriteTarget{}, false
	}
	// An action that is not http(s) or a path means the page builds the request in
	// script; that is exactly the case the configured override exists for.
	if parsed, err := url.Parse(action); err != nil ||
		(parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https") {
		return WriteTarget{}, false
	}
	method := "POST"
	if m := reMethod.FindStringSubmatch(form); m != nil {
		if value, ok := firstGroup(m); ok && strings.TrimSpace(value) != "" {
			method = strings.ToUpper(strings.TrimSpace(value))
		}
	}
	return WriteTarget{
		Method: method,
		URL:    strings.TrimSpace(html.UnescapeString(action)),
		Source: "选座页表单",
		Note:   "提交表单 action",
	}, true
}

// formAt returns the whole <form ...>...</form> element starting at loc[0].
func formAt(body string, loc []int) string {
	rest := body[loc[0]:]
	if end := reFormEnd.FindStringIndex(rest); end != nil {
		return rest[:end[0]]
	}
	return rest
}

// formContaining returns the form element that contains the given id, preferring
// the innermost (shortest) match so a nested form cannot hide the real one.
func formContaining(body, id string) string {
	needle := `id="` + id + `"`
	best := ""
	for _, loc := range reFormTag.FindAllStringIndex(body, -1) {
		form := formAt(body, loc)
		if !strings.Contains(form, needle) && !strings.Contains(form, `id='`+id+`'`) {
			continue
		}
		if best == "" || len(form) < len(best) {
			best = form
		}
	}
	return best
}

// pageTargets is everything one seat page named, submit form first.
type pageTargets struct {
	submit WriteTarget
	hasSub bool
}

// parsePageTargets reads the seat page once for every write target it names.
func parsePageTargets(body string) pageTargets {
	targets := pageTargets{}
	targets.submit, targets.hasSub = parseSubmitTarget(body)
	if !targets.hasSub {
		targets.submit, targets.hasSub = parseScriptSubmitTarget(body)
	}
	return targets
}

// resolveTarget turns a page-relative action into an absolute office URL, and
// refuses anything that is not on the office host.
func resolveTarget(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if !u.IsAbs() {
		u, err = url.Parse(Origin + "/" + strings.TrimPrefix(u.Path, "/"))
		if err != nil {
			return "", false
		}
		// Keep the page's query string: some forms carry it in the action.
		if q := strings.TrimSpace(raw); strings.Contains(q, "?") {
			if parsed, err := url.Parse(Origin + "/" + strings.TrimPrefix(q, "/")); err == nil {
				u = parsed
			}
		}
	}
	if u.Scheme != "https" || u.User != nil || !strings.EqualFold(u.Hostname(), OfficeHost) {
		return "", false
	}
	return u.String(), true
}

// --- client side -----------------------------------------------------------

// ReserveConfig is the user's own statement about write targets. It exists so the
// feature still works for a page whose form is built by JavaScript: the user can
// paste the request they saw in their browser's network panel.
type ReserveConfig struct {
	// SubmitURL override whatever the page named. Empty means "use the
	// page's own target".
	SubmitURL string
	// CancelURL overrides the path the seat page's script uses.
	CancelURL string
	// Method is used for the configured URLs; empty means POST.
	Method string
}

// ReserveTargets describes where a reservation could be sent for the open room.
type ReserveTargets struct {
	Submit    WriteTarget
	HasSubmit bool
	// Source explains where the submit target came from.
	Source string
}

// ReserveResult is what the service said about a reservation attempt. Nothing is
// inferred: when the response is not a shape we know, the raw status and a short
// excerpt are reported instead.
type ReserveResult struct {
	Target   string
	Method   string
	Path     string
	Status   int
	OK       bool
	Rejected bool
	Message  string
	At       time.Time
}

// setPageTargets records what the seat page named. It is called on every room
// open, and it invalidates the previous room's armed write target.
func (c *SeatClient) setPageTargets(targets pageTargets) {
	c.submitTarget, c.hasSubmitTarget = targets.submit, targets.hasSub
	if c.submitTarget.URL != "" {
		if resolved, ok := resolveTarget(c.submitTarget.URL); ok {
			c.submitTarget.URL = resolved
		} else {
			c.submitTarget, c.hasSubmitTarget = WriteTarget{}, false
		}
	}
	if c.Login != nil && c.Login.Session != nil && c.Login.Session.Guard != nil {
		c.Login.Session.Guard.Disarm()
	}
}

// SetReserveConfig applies the user's overrides. Configured targets win over the
// page's, because the user is the one who can see their own browser.
func (c *SeatClient) SetReserveConfig(cfg ReserveConfig) {
	c.reserve = cfg
}

// ReserveTargets reports every target currently available for the open room.
func (c *SeatClient) ReserveTargets() ReserveTargets {
	out := ReserveTargets{
		HasSubmit: c.hasSubmitTarget,
		Submit:    c.submitTarget,
		Source:    c.submitTarget.Source,
	}
	method := strings.ToUpper(strings.TrimSpace(c.reserve.Method))
	if method == "" {
		method = "POST"
	}
	if raw := strings.TrimSpace(c.reserve.SubmitURL); raw != "" {
		if resolved, ok := resolveTarget(raw); ok {
			out.Submit = WriteTarget{Method: method, URL: resolved, Source: "设置", Note: "设置中的预约接口"}
			out.HasSubmit = true
			out.Source = "设置"
		}
	}

	return out
}

// SubmitReservation sends the prepared seat choice. It refuses unless a target is
// known and the transport can arm exactly that target.
func (c *SeatClient) SubmitReservation(ctx context.Context) (*ReserveResult, error) {
	if !LocalSchoolAccess {
		return nil, block("仅 TUI 版本不能执行预约；请交由后端执行")
	}
	if c.Remote != nil {
		return nil, fmt.Errorf("远程模式请创建后端预订任务")
	}
	if c.Current == nil {
		return nil, inputErr("还没有选座预览")
	}
	targets := c.ReserveTargets()
	if !targets.HasSubmit {
		return nil, unsupportedErr("选座页没有提供提交地址：请在设置里填写预约接口地址" +
			"（浏览器 Network 面板里的那条请求），或确认该房间的选座页是否已改版")
	}
	return c.sendWrite(ctx, targets.Submit, c.Current.payload, "预约 "+c.Current.SeatNum+" 号")
}

// sendWrite arms exactly one target and sends one request to it.
func (c *SeatClient) sendWrite(ctx context.Context, target WriteTarget, payload []pair, what string) (*ReserveResult, error) {
	if c.Login == nil || c.Login.Session == nil {
		return nil, opErr("会话尚未就绪")
	}
	session := c.Login.Session
	if session.Guard == nil {
		return nil, opErr("会话缺少传输策略")
	}
	method := strings.ToUpper(target.Method)
	if method == "" {
		method = "POST"
	}
	action, err := session.Guard.Arm(method, target.URL)
	if err != nil {
		return nil, err
	}

	defer session.Guard.Disarm()
	values := url.Values{}
	for _, p := range payload {
		values.Set(p.Key, p.Value)
	}
	var resp *Response
	if action.Method == "GET" {
		resp, err = session.Get(ctx, target.URL, values)
	} else {
		resp, err = session.PostForm(ctx, target.URL, values)
	}
	if err != nil {
		return nil, opWrap(err, "发送%s失败", what)
	}
	ok, message := interpretWriteResponse(resp)
	result := &ReserveResult{
		Target:   target.URL,
		Method:   action.Method,
		Path:     action.Path,
		Status:   resp.StatusCode,
		OK:       ok,
		Rejected: explicitRejection(resp),
		Message:  message,
		At:       time.Now(),
	}
	if ok {
		if c.Current != nil {
			c.Current.Submitted = true
		}
	}
	return result, nil
}

// interpretWriteResponse reads the service's own verdict. A response shape we do
// not recognise is reported as "sent, not confirmed" rather than as success.
func writeVerdict(resp *Response) (known, ok bool, message string) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, false, "HTTP " + strconv.Itoa(resp.StatusCode) + "，结果未确认"
	}
	var obj map[string]any
	if json.Unmarshal(resp.Body, &obj) != nil {
		return false, false, "响应不是 JSON，结果未确认"
	}
	v, found := obj["status"]
	if !found {
		v, found = obj["success"]
	}
	message = firstString(obj, "msg", "message", "errorMsg")
	if !found {
		return false, false, "响应缺少明确状态，结果未确认"
	}
	switch x := v.(type) {
	case bool:
		known, ok = true, x
	case float64:
		known = x == 0 || x == 1
		ok = x == 1
	case string:
		switch strings.ToLower(x) {
		case "true", "1", "success", "ok":
			known, ok = true, true
		case "false", "0":
			known = true
		}
	}
	if message == "" {
		if ok {
			message = "服务端确认成功"
		} else if known {
			message = "服务端明确拒绝"
		} else {
			message = "结果未确认"
		}
	}
	return
}
func explicitRejection(resp *Response) bool { k, ok, _ := writeVerdict(resp); return k && !ok }
func interpretWriteResponse(resp *Response) (bool, string) {
	_, ok, msg := writeVerdict(resp)
	return ok, msg
}

func firstString(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key]; ok {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return ""
}

func truthy(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "ok", "success", "yes":
			return true
		}
	}
	return false
}

// clipText flattens a response excerpt for display: markup and newlines removed,
// then clipped by cells in the caller.
func clipText(text string, limit int) string {
	text = reTag.ReplaceAllString(text, " ")
	text = strings.Join(strings.Fields(html.UnescapeString(text)), " ")
	if len(text) > limit {
		text = text[:limit] + "…"
	}
	return text
}

var reTag = regexp.MustCompile(`(?s)<[^>]*>`)

// --- reservations the service reports ---------------------------------------

// Reservation is one live reservation, as /data/apps/seat/index reports it. Every
// field comes from the service; nothing is derived except the split of the
// timestamps into a day and a time range, which is what a person reads.
type Reservation struct {
	ID       int
	SeatNum  string
	RoomID   int
	Room     string
	Day      string
	Start    string
	End      string
	Status   string
	Duration float64
	// StatusCode is the service's own number, kept for the diagnostics view.
	StatusCode int
}

// rawReservation mirrors the fields the seat page's template reads.
type rawReservation struct {
	ID              any     `json:"id"`
	SeatNum         any     `json:"seatNum"`
	RoomID          any     `json:"roomId"`
	FirstLevelName  string  `json:"firstLevelName"`
	SecondLevelName string  `json:"secondLevelName"`
	ThirdLevelName  string  `json:"thirdLevelName"`
	StartTime       any     `json:"startTime"`
	EndTime         any     `json:"endTime"`
	Duration        float64 `json:"duration"`
	Status          *int    `json:"status"`
}

// CurrentReserves reads the reservations the service says are live. It is a
// read-only query against the whitelisted index endpoint -- the same call the
// official page makes to draw its "当前预约" cards.
func (c *SeatClient) CurrentReserves(ctx context.Context) ([]Reservation, error) {
	raw, err := c.jsonCall(ctx, http.MethodGet, "/data/apps/seat/index", url.Values{
		"fidEnc": {c.FIDEnc},
	})
	if err != nil {
		return nil, err
	}
	// jsonCall hands back the envelope's data object, not the envelope.
	var payload struct {
		CurReserves []rawReservation `json:"curReserves"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, opErr("当前预约响应无法解析")
	}
	out := make([]Reservation, 0, len(payload.CurReserves))
	for _, item := range payload.CurReserves {
		start, startRaw := serviceTime(item.StartTime)
		end, _ := serviceTime(item.EndTime)
		room := []string{}
		for _, part := range []string{item.FirstLevelName, item.SecondLevelName, item.ThirdLevelName} {
			if part != "" {
				room = append(room, part)
			}
		}
		reservation := Reservation{
			ID:       atoiSafe(seatNumString(item.ID)),
			SeatNum:  zfill3(seatNumString(item.SeatNum)),
			RoomID:   atoiSafe(seatNumString(item.RoomID)),
			Room:     strings.Join(room, " - "),
			Duration: item.Duration,
			Status:   reservationStatus(item.Status),
		}
		if item.Status != nil {
			reservation.StatusCode = *item.Status
		}
		if !start.IsZero() {
			reservation.Day = start.Format("2006-01-02")
			reservation.Start = start.Format("15:04")
		} else if startRaw != "" {
			reservation.Day = startRaw
		}
		if !end.IsZero() {
			reservation.End = end.Format("15:04")
		}
		out = append(out, reservation)
	}
	return out, nil
}

// reservationStatus names the service's status code the way its own page does.
func reservationStatus(code *int) string {
	if code == nil {
		return "未知"
	}
	switch *code {
	case 0:
		return "待履约"
	case 1:
		return "使用中"
	case 3:
		return "暂离中"
	case 5:
		return "被监督中"
	}
	return "其他"
}

// serviceTime reads a timestamp that may be an epoch in milliseconds or seconds,
// or a formatted string. The service uses both across deployments.
func serviceTime(value any) (time.Time, string) {
	switch v := value.(type) {
	case float64:
		return time.UnixMilli(int64(v)).In(Shanghai), ""
	case string:
		if v == "" {
			return time.Time{}, ""
		}
		if parsed, err := time.ParseInLocation("2006-01-02 15:04:05", v, Shanghai); err == nil {
			return parsed, v
		}
		if parsed, err := time.ParseInLocation("2006-01-02 15:04", v, Shanghai); err == nil {
			return parsed, v
		}
		return time.Time{}, v
	}
	return time.Time{}, ""
}

func atoiSafe(text string) int {
	value, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0
	}
	return value
}

// CancelTarget is where a cancellation goes. The seat page's own script posts it
// with operateData, which is jQuery's getJSON, so this is a GET with the
// reservation id -- not a guess, a reading of the page that is on screen.
func (c *SeatClient) CancelTarget() (WriteTarget, bool) {
	if c.roomData == nil && c.windowStatus == "" {
		// Nothing has been loaded from the page yet, so nothing was read from it.
		return WriteTarget{}, false
	}
	if raw := strings.TrimSpace(c.reserve.CancelURL); raw != "" {
		if resolved, ok := resolveTarget(raw); ok {
			return WriteTarget{Method: "GET", URL: resolved, Source: "设置", Note: "设置中的取消接口"}, true
		}
	}
	return WriteTarget{
		Method: "GET",
		URL:    Origin + "/data/apps/seat/cancel",
		Source: "选座页脚本",
		Note:   "页面 askCancel 的 operateData('data/apps/seat/cancel')",
	}, true
}

// CancelReservation performs the cancellation, after the same arming and
// confirmation every other write goes through.
func (c *SeatClient) CancelReservation(ctx context.Context, id int) (*ReserveResult, error) {
	if id <= 0 {
		return nil, inputErr("预约记录缺少 id，无法取消")
	}
	target, ok := c.CancelTarget()
	if !ok {
		return nil, unsupportedErr("尚未打开选座页，无从确认取消接口；先在房间面板打开一次自习室")
	}
	payload := []pair{{Key: "id", Value: strconv.Itoa(id)}}
	return c.sendWrite(ctx, target, payload, "取消预约")
}
