package chaoxing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// AuthURL is the official WFU OAuth entry point.
const AuthURL = "https://e.wfu.edu.cn/oauth2/authorize?response_type=redirect&redirect_uri=https://tyrzfw.chaoxing.com/OAuth2/wfu/index&client_id=6f2c5bd1a3614fef8493100be288f709&state=1"

// allowedHosts mirrors qr_login.ALLOWED_HOSTS.
var allowedHosts = map[string]bool{
	"e.wfu.edu.cn":           true,
	"tyrzfw.chaoxing.com":    true,
	"passport2.chaoxing.com": true,
	"office.chaoxing.com":    true,
}

// BrowserUserAgent is required: the callback pages reject a default HTTP client
// user agent.
const BrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/153.0.0.0 Safari/537.36"

const (
	qrCodeEndpoint  = "https://e.wfu.edu.cn/ssoApi/appQRCode"
	qrCheckEndpoint = "https://e.wfu.edu.cn/ssoApi/checkQRLogin"
)

// QRLogin drives the official QR login flow. It wraps a guarded Session so the
// read-only policy applies to every hop, including redirects.
type QRLogin struct {
	Session *Session

	UUID         string
	Tokens       map[string]string
	LastResponse *Response

	// SeatPageResponse mirrors login.seat_page_response.
	SeatPageResponse *Response

	CallbackAttempted bool
	CallbackResult    map[string]any

	mu                sync.Mutex
	webLoginAttempted bool
	webLoginResult    map[string]any
	webLoginPayload   any
}

// NewQRLogin creates a session with the browser user agent and the read-only
// guard installed. An empty proxy means direct connection; environment proxies
// are deliberately ignored, matching the Python client's trust_env=False.
func NewQRLogin(proxy, logDir string) (*QRLogin, error) {
	session, err := NewSession(proxy, logDir)
	if err != nil {
		return nil, err
	}
	session.UserAgent = BrowserUserAgent
	return &QRLogin{
		Session: session,
		Tokens:  map[string]string{},
	}, nil
}

// Close releases held resources.
func (l *QRLogin) Close() error { return l.Session.Close() }

// BlockedSeatRequests reports how many requests the read-only policy refused.
func (l *QRLogin) BlockedSeatRequests() int64 { return l.Session.BlockedSeatRequests() }

var jsStringReCache sync.Map

func jsStringRe(name string) *regexp.Regexp {
	if cached, ok := jsStringReCache.Load(name); ok {
		return cached.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`\bvar\s+` + regexp.QuoteMeta(name) + `\s*=\s*("(?:\\.|[^"\\])*")`)
	jsStringReCache.Store(name, re)
	return re
}

// readJSString extracts `var <name> = "<json string>"` from a page.
func readJSString(body, name string) (string, error) {
	m := jsStringRe(name).FindStringSubmatch(body)
	if m == nil {
		return "", opErr("扫码页面未找到 " + name + "；请检查页面结构或响应状态")
	}
	var out string
	if err := json.Unmarshal([]byte(m[1]), &out); err != nil {
		return "", opErr("扫码页面 " + name + " 不是有效的字符串")
	}
	return out, nil
}

// Start requests a fresh QR code and returns the PNG bytes for rendering.
func (l *QRLogin) Start(ctx context.Context) ([]byte, error) {
	resp, err := l.Session.Get(ctx, qrCodeEndpoint, url.Values{"locationurl": {AuthURL}})
	if err != nil {
		return nil, opWrap(err, "无法获取二维码")
	}
	if resp.StatusCode >= 400 {
		return nil, opErrf("无法获取二维码: HTTP %d", resp.StatusCode)
	}
	body := resp.Text()
	uuid, err := readJSString(body, "UUID")
	if err != nil {
		return nil, err
	}
	dataURI, err := readJSString(body, "baseImg")
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(dataURI, "data:image/png;base64,") {
		return nil, opErr("二维码格式发生变化")
	}
	png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(dataURI, "data:image/png;base64,"))
	if err != nil {
		return nil, opErr("二维码 base64 数据损坏")
	}
	l.UUID = uuid
	l.LastResponse = resp
	return png, nil
}

// QRState is the outcome of one poll.
type QRState string

const (
	QRWaiting   QRState = "waiting"
	QRConfirmed QRState = "confirmed"
	QRExpired   QRState = "expired"
)

// PollOnce performs a single scan-status check.
func (l *QRLogin) PollOnce(ctx context.Context) (QRState, string, error) {
	if l.UUID == "" {
		return "", "", inputErr("请先生成二维码")
	}
	resp, err := l.Session.PostForm(ctx, qrCheckEndpoint, url.Values{
		"uuid":        {l.UUID},
		"locationurl": {AuthURL},
	})
	if err != nil {
		return "", "", opWrap(err, "扫码状态查询失败")
	}
	if resp.StatusCode >= 400 {
		return "", "", opErrf("扫码状态查询失败: HTTP %d", resp.StatusCode)
	}
	var result struct {
		Code string `json:"code"`
		Data struct {
			LocatURL string `json:"locatUrl"`
		} `json:"data"`
	}
	if err := resp.Decode(&result); err != nil {
		return "", "", opErr("扫码状态响应不是 JSON")
	}
	switch result.Code {
	case "0x000000":
		if result.Data.LocatURL == "" {
			return "", "", opErr("登录成功响应缺少 locatUrl")
		}
		return QRConfirmed, result.Data.LocatURL, nil
	case "0x0030010016":
		return QRExpired, "", nil
	}
	return QRWaiting, "", nil
}

// FollowCallback walks the OAuth redirect chain, stopping before any request
// that could leave the login flow. It never calls a seat reservation endpoint.
func (l *QRLogin) FollowCallback(ctx context.Context, target string) (map[string]any, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.CallbackAttempted {
		if l.CallbackResult != nil {
			return l.CallbackResult, nil
		}
		return map[string]any{
			"state":      "callback_already_attempted",
			"diagnostic": l.ResponseDiagnostic(l.LastResponse),
		}, nil
	}
	l.CallbackAttempted = true

	chain := []map[string]any{}
	current := target
	for hop := 0; hop < 12; hop++ {
		u, err := url.Parse(current)
		if err != nil {
			return nil, opErr("回调地址无法解析")
		}
		host := strings.ToLower(u.Hostname())
		if u.Scheme != "https" || !allowedHosts[host] {
			return map[string]any{"state": "inspect_redirect", "host": host, "path": u.Path}, nil
		}
		if strings.Contains(u.Path, "/data/apps/seat/") || strings.HasSuffix(u.Path, "/submit") {
			return nil, opErr("登录流程禁止调用预约接口")
		}
		for key, values := range u.Query() {
			if strings.Contains(strings.ToLower(key), "token") && len(values) > 0 {
				l.Tokens[key] = values[len(values)-1]
			}
		}
		resp, err := l.Session.Get(ctx, current, nil)
		if err != nil {
			return nil, opWrap(err, "回调请求失败")
		}
		l.LastResponse = resp
		chain = append(chain, map[string]any{"host": host, "path": u.Path, "status": resp.StatusCode})

		if resp.StatusCode >= 400 {
			result := map[string]any{
				"state":      "callback_http_error",
				"chain":      chain,
				"diagnostic": l.ResponseDiagnostic(resp),
				"message":    "扫码已确认，但回调失败；未自动重试授权码。",
			}
			l.CallbackResult = result
			return result, nil
		}
		if resp.IsRedirect() {
			next, err := resolveURL(resp.URL.String(), resp.Location())
			if err != nil {
				return nil, opErr("回调跳转地址无效")
			}
			current = next
			continue
		}
		if host == "tyrzfw.chaoxing.com" && u.Path == "/OAuth2/wfu/index" {
			result, err := l.completeWebLogin(ctx)
			if err != nil {
				return nil, err
			}
			result["chain"] = chain
			l.CallbackResult = result
			return result, nil
		}
		return map[string]any{
			"state":        "callback_loaded",
			"chain":        chain,
			"token_names":  sortedKeys(l.Tokens),
			"cookie_names": l.Session.CookieNames(current),
			"note":         "未发现 token 不等于失败；可能使用 Cookie 会话或还需分析页面跳转。",
		}, nil
	}
	return nil, opErr("回调跳转超过限制")
}

// ResponseDiagnostic inspects an existing response without making a request.
func (l *QRLogin) ResponseDiagnostic(r *Response) map[string]any {
	if r == nil {
		return map[string]any{"state": "no_response"}
	}
	host, path := "", ""
	if r.URL != nil {
		host, path = strings.ToLower(r.URL.Hostname()), r.URL.Path
	}
	body := strings.ToLower(r.Text())
	indicators := []string{}
	for _, word := range []string{
		"access denied", "forbidden", "proxy", "cloudflare", "nginx",
		"invalid code", "expired", "验证码", "访问被拒绝", "授权码",
	} {
		if strings.Contains(body, word) {
			indicators = append(indicators, word)
		}
	}
	return map[string]any{
		"host":            host,
		"path":            path,
		"status":          r.StatusCode,
		"content_type":    r.Header.Get("Content-Type"),
		"server":          r.Header.Get("Server"),
		"via":             r.Header.Get("Via"),
		"body_bytes":      len(r.Body),
		"body_indicators": indicators,
		"cookie_names":    r.CookieNames(),
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// StateString renders a callback result's state for display.
func StateString(result map[string]any) string {
	if result == nil {
		return "unknown"
	}
	if s, ok := result["state"].(string); ok {
		return s
	}
	return "unknown"
}
