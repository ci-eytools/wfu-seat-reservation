package chaoxing

import (
	"context"
	"html"
	"net/url"
	"regexp"
	"strings"
)

// SeatPageResult is the outcome of the read-only seat landing page check.
type SeatPageResult struct {
	State           string            `json:"state"`
	Status          int               `json:"status,omitempty"`
	Title           *string           `json:"title,omitempty"`
	Target          string            `json:"target,omitempty"`
	Evidence        *SeatPageEvidence `json:"evidence,omitempty"`
	Chain           []SeatPageHop     `json:"chain"`
	WithoutWfwToken bool              `json:"without_wfw_token"`
	Note            string            `json:"note,omitempty"`
}

// SeatPageHop is one hop of the (bounded) redirect chain.
type SeatPageHop struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
}

// SeatPageEvidence records why a page was accepted as the logged-in seat page.
type SeatPageEvidence struct {
	SeatTitle       bool `json:"seat_title"`
	SeatScript      bool `json:"seat_script"`
	UserInfoPresent bool `json:"user_info_present"`
}

var (
	reTitle      = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reSeatScript = regexp.MustCompile(`(?i)<script[^>]+src=["'][^"']*apps/seat/`)
	reUserInfo   = regexp.MustCompile(`userLoginInfo\s*=\s*\{\s*"userInfo"\s*:\s*\{`)
)

func extractTitle(body string) *string {
	m := reTitle.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	title := strings.TrimSpace(html.UnescapeString(m[1]))
	return &title
}

// CheckSeatPage loads the seat landing page read-only and verifies that it is
// really the logged-in seat page. Redirects are followed manually, at most six
// times, and only while they stay on the seat landing path.
func CheckSeatPage(ctx context.Context, login *QRLogin, rawURL string, withoutWfwToken bool) (*SeatPageResult, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || strings.ToLower(u.Hostname()) != OfficeHost ||
		u.Path != "/front/third/apps/seat/index" {
		return nil, inputErr("请输入指定的 HTTPS 座位首页地址")
	}
	if withoutWfwToken {
		query := u.Query()
		query.Del("wfw_token")
		u.RawQuery = query.Encode()
		rawURL = u.String()
	}

	chain := []SeatPageHop{}
	current := rawURL
	for hop := 0; hop < 6; hop++ {
		p, err := url.Parse(current)
		if err != nil || p.Scheme != "https" || strings.ToLower(p.Hostname()) != OfficeHost ||
			p.Path != "/front/third/apps/seat/index" {
			// Stop before sending any redirected request outside the seat homepage.
			return &SeatPageResult{State: "redirected_away", Target: safeURL(current), Chain: chain}, nil
		}
		resp, err := login.Session.Get(ctx, current, nil)
		if err != nil {
			return nil, opWrap(err, "座位首页请求失败")
		}
		login.SeatPageResponse = resp
		chain = append(chain, SeatPageHop{URL: safeURL(resp.URL.String()), Status: resp.StatusCode})

		if resp.IsRedirect() {
			next, err := resolveURL(resp.URL.String(), resp.Location())
			if err != nil {
				return nil, opErr("座位首页跳转地址无效")
			}
			current = next
			continue
		}

		body := resp.Text()
		title := extractTitle(body)
		evidence := SeatPageEvidence{
			SeatTitle:       title != nil && strings.Contains(*title, "座位"),
			SeatScript:      reSeatScript.MatchString(body),
			UserInfoPresent: reUserInfo.MatchString(body),
		}
		state := "needs_inspection"
		if resp.StatusCode == 200 && evidence.SeatTitle && evidence.SeatScript && evidence.UserInfoPresent {
			state = "seat_page_loaded"
		}
		result := &SeatPageResult{
			State:           state,
			Status:          resp.StatusCode,
			Title:           title,
			Evidence:        &evidence,
			Chain:           chain,
			WithoutWfwToken: withoutWfwToken,
			Note:            "仅 GET 座位首页；未调用预约接口。页面内容保存在 seat_page_response。",
		}
		// Like the original, a non-accepted page is reported, not raised; the
		// caller decides whether that state is fatal.
		return result, nil
	}
	return &SeatPageResult{State: "redirect_limit", Chain: chain}, nil
}
