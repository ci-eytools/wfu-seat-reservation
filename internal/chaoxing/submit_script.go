package chaoxing

import (
	"context"
	"html"
	"net/url"
	"regexp"
	"strings"
)

var scriptTagRE = regexp.MustCompile(`(?is)<script\b[^>]*\bsrc\s*=\s*["']([^"']+)["'][^>]*>`)
var submitCallRE = regexp.MustCompile(`operateData\(\s*["'](/?data/apps/seat/submit)["']\s*,\s*\{`)
var postHelperRE = regexp.MustCompile(`(?s)function\s+operateData\s*\(\s*url\s*,\s*data\s*\)\s*\{\s*return\s+\$\.post\(\s*url\s*,\s*data\s*\)`)

func parseScriptSubmitTarget(body string) (WriteTarget, bool) {
	call := submitCallRE.FindStringSubmatch(body)
	if len(call) < 2 || !postHelperRE.MatchString(body) || !strings.Contains(body, "submitVerify.verifyParam") {
		return WriteTarget{}, false
	}
	return WriteTarget{Method: "POST", URL: Origin + "/" + strings.TrimPrefix(call[1], "/"), Source: "选座页脚本", Note: "operateData 调用及 $.post 定义"}, true
}
func (c *SeatClient) loadScriptTarget(ctx context.Context, page string, targets pageTargets) pageTargets {
	for _, m := range scriptTagRE.FindAllStringSubmatch(page, -1) {
		raw := html.UnescapeString(m[1])
		if strings.HasPrefix(raw, "//") {
			raw = "https:" + raw
		}
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Host != "office-static.chaoxing.com" || u.User != nil || !strings.HasPrefix(u.Path, "/r/staticreserve/js/") || !strings.HasSuffix(u.Path, "/seat_select_third.js") {
			continue
		}
		r, e := c.Login.Session.Get(ctx, u.String(), nil)
		if e != nil || r.StatusCode != 200 {
			return targets
		}
		target, ok := parseScriptSubmitTarget(r.Text())
		if ok {
			targets.submit = target
			targets.hasSub = true
		}
		return targets
	}
	return targets
}
