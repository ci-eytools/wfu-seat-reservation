// Package chaoxing is a Go port of the project's seat client.
//
// # Transport policy
//
// The Python original was read-only, and its whitelist is kept here verbatim:
// every query goes through GuardedTransport and is checked against readAPIs, and a
// request that is not on the list is refused before any network I/O. Because
// net/http routes every redirect hop through RoundTrip, redirected requests are
// checked too.
//
// # Writes
//
// A reservation is a write, and it is deliberately not covered by a whitelist of
// guessed paths: inventing an endpoint for somebody else's service is how a
// client silently does the wrong thing. Instead a write target has to be armed:
//
//   - it comes from the seat page the user is already looking at (that page's own
//     form action) or from the user's configuration;
//   - it has to live under the seat API namespace on the office host;
//   - it is printed in full in a confirmation before anything is sent.
//
// Until a target is armed the transport refuses it exactly as before, so the
// no-writes property still holds for every path nobody has named.
package chaoxing

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// OfficeHost is the only host that serves seat pages and seat APIs.
const OfficeHost = "office.chaoxing.com"

// seatAPIPrefix is the namespace an armed write target must live in; a page
// action pointing anywhere else is refused rather than armed.
const seatAPIPrefix = "/data/apps/seat"

// ReadAPI is one whitelisted (method, path) pair.
type ReadAPI struct {
	Method string
	Path   string
}

// readAPIs mirrors seat_guard.READ_APIS exactly. Paths are lower case, as in the
// original, and are compared against the decoded, cleaned, lower-cased path.
var readAPIs = map[ReadAPI]bool{
	{http.MethodGet, "/data/apps/seat/config"}:                    true,
	{http.MethodGet, "/data/apps/seat/index"}:                     true,
	{http.MethodGet, "/data/apps/seat/room/list"}:                 true,
	{http.MethodGet, "/data/apps/seat/room/reserve-window/check"}: true,
	{http.MethodPost, "/data/apps/seat/room/info"}:                true,
	{http.MethodPost, "/data/apps/seat/room/info/switch"}:         true,
	{http.MethodPost, "/data/apps/seat/getusedtimes"}:             true,
	{http.MethodPost, "/data/apps/seat/getusedseatnums"}:          true,
	{http.MethodGet, "/data/apps/seat/getdrawseat"}:               true,
	{http.MethodGet, "/data/apps/seat/seatgrid/roomid"}:           true,
}

// seatPages mirrors seat_guard's read-only office page whitelist.
var seatPages = map[string]bool{
	"/front/third/apps/seat/index":  true,
	"/front/third/apps/seat/list":   true,
	"/front/third/apps/seat/select": true,
}

// BlockedSeatRequest reports a request that the read-only policy refused. It is
// the Go counterpart of the Python BlockedSeatRequest exception.
type BlockedSeatRequest struct{ Reason string }

func (e *BlockedSeatRequest) Error() string { return e.Reason }

func block(reason string) error { return &BlockedSeatRequest{Reason: reason} }

// CheckRequest mirrors seat_guard.check_request: it decodes the path up to three
// times (defeating percent-encoding tricks), normalises it, and then applies the
// read-only whitelist. It never performs network I/O.
func CheckRequest(method, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		// Fail closed: an unparseable URL cannot be proven read-only.
		return block("已拦截无法解析的请求地址")
	}

	p := u.Path
	for i := 0; i < 3; i++ {
		if decoded, err := url.PathUnescape(p); err == nil {
			p = decoded
		}
	}
	p = strings.ToLower(path.Clean(p))
	m := strings.ToUpper(method)
	host := strings.ToLower(u.Hostname())

	if strings.Contains(p, "/data/apps/seat") {
		if host != OfficeHost || !readAPIs[ReadAPI{m, p}] {
			return block("已在发送前拦截非只读座位接口")
		}
	}
	if host == OfficeHost {
		if !readAPIs[ReadAPI{m, p}] && !(m == http.MethodGet && seatPages[p]) {
			return block("已拦截未确认的 office 接口")
		}
	}
	return nil
}

// GuardedTransport wraps an http.RoundTripper with the transport policy. It is
// the Go equivalent of seat_guard.guard_session, which patched Session.send.
type GuardedTransport struct {
	base    http.RoundTripper
	blocked atomic.Int64
	log     *LoginLog

	// mu guards armed, the explicitly named write targets.
	mu    sync.RWMutex
	armed map[ReadAPI]bool
}

// NewGuardedTransport wraps base. A nil base means http.DefaultTransport.
func NewGuardedTransport(base http.RoundTripper) *GuardedTransport {
	if base == nil {
		base = http.DefaultTransport
	}
	return &GuardedTransport{base: base, armed: map[ReadAPI]bool{}}
}

// Arm names one write target the transport may send. The target is validated
// here, so a page cannot hand over a request to some other service, and it stays
// armed only for this session.
func (t *GuardedTransport) Arm(method, rawURL string) (ReadAPI, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ReadAPI{}, block("无法解析要启用的写接口地址")
	}
	p := strings.ToLower(path.Clean(u.Path))
	m := strings.ToUpper(method)
	if m == "" {
		m = http.MethodPost
	}
	switch {
	case u.Scheme != "https" || u.User != nil:
		return ReadAPI{}, block("写接口必须使用 HTTPS 且不能含用户凭据")
	case m != http.MethodPost && m != http.MethodGet:
		return ReadAPI{}, block("只允许 GET/POST 写接口")
	case strings.ToLower(u.Hostname()) != OfficeHost:
		return ReadAPI{}, block("写接口必须位于 " + OfficeHost)
	case !strings.HasPrefix(p, seatAPIPrefix+"/"):
		return ReadAPI{}, block("写接口必须位于 " + seatAPIPrefix + " 之内")
	case seatPages[p]:
		return ReadAPI{}, block("页面地址不能作为写接口")
	}
	action := ReadAPI{Method: m, Path: p}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.armed == nil {
		t.armed = map[ReadAPI]bool{}
	}
	t.armed[action] = true
	return action, nil
}

// Disarm forgets every armed write target. It is called when the session changes
// room, because the page that named the targets is gone by then.
func (t *GuardedTransport) Disarm() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.armed = map[ReadAPI]bool{}
}

// Armed reports whether a (method, path) pair is currently allowed to be sent.
func (t *GuardedTransport) Armed(method, rawURL string) (ReadAPI, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ReadAPI{}, false
	}
	// Arming is host-scoped: the same path on another host is a different request.
	if !strings.EqualFold(u.Hostname(), OfficeHost) {
		return ReadAPI{}, false
	}
	action := ReadAPI{Method: strings.ToUpper(method), Path: strings.ToLower(path.Clean(u.Path))}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return action, t.armed[action]
}

// RoundTrip enforces the transport policy before delegating to the base
// transport, then (optionally) records the response in the redacted log.
func (t *GuardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !LocalSchoolAccess {
		return nil, block("仅 TUI 版本不允许直接访问学校服务")
	}
	if err := CheckRequest(req.Method, req.URL.String()); err != nil {
		// The read-only check failed; the request is still allowed if the user
		// has explicitly armed exactly this target.
		if _, ok := t.Armed(req.Method, req.URL.String()); !ok {
			t.blocked.Add(1)
			return nil, err
		}
	}
	// Buffer the outgoing form body only while logging is enabled, so secrets in
	// POST bodies can be redacted. The body is restored before it is sent.
	var reqBody []byte
	if t.log != nil && req.Body != nil {
		if b, err := readAllLimited(req.Body, 1<<20); err == nil {
			reqBody = b
			req.Body = io.NopCloser(bytes.NewReader(b))
		}
	}
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if t.log != nil {
		t.log.Record(req, resp, reqBody, time.Since(start))
	}
	return resp, nil
}

// BlockedCount reports how many requests the policy refused. It is the
// counterpart of Python's session.blocked_seat_requests.
func (t *GuardedTransport) BlockedCount() int64 { return t.blocked.Load() }

// SetLog attaches the redacted diagnostics logger. A nil log disables logging.
func (t *GuardedTransport) SetLog(l *LoginLog) { t.log = l }

// Log returns the attached logger, which may be nil.
func (t *GuardedTransport) Log() *LoginLog { return t.log }
