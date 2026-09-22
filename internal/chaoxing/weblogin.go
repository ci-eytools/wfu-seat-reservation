package chaoxing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// callbackFields mirrors web_login_step.FIELDS, in the same order, so the
// generated query string preserves the original parameter order.
var callbackFields = []string{"data", "time", "enc", "displayName", "userRole"}

type pair struct {
	Key   string
	Value string
}

// encodePairs percent-encodes pairs in order, the counterpart of urlencode on an
// insertion-ordered dict.
func encodePairs(pairs []pair) string {
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, url.QueryEscape(p.Key)+"="+url.QueryEscape(p.Value))
	}
	return strings.Join(parts, "&")
}

var (
	reCallbackURL = regexp.MustCompile(`var\s+url\s*=\s*("(?:\\.|[^"\\])*")`)
	reDataBlock   = regexp.MustCompile(`(?s)\bdata\s*:\s*\{(.*?)\}\s*,\s*dataType`)
)

var callbackFieldReCache = func() func(string) *regexp.Regexp {
	var (
		mu    sync.Mutex
		cache = map[string]*regexp.Regexp{}
	)
	return func(field string) *regexp.Regexp {
		mu.Lock()
		defer mu.Unlock()
		if re, ok := cache[field]; ok {
			return re
		}
		re := regexp.MustCompile(`(?:^|[^\w])` + regexp.QuoteMeta(field) + `\s*:\s*("(?:\\.|[^"\\])*"|-?\d+)\s*[,}]`)
		cache[field] = re
		return re
	}
}()

// callbackFieldRe builds an RE2-compatible equivalent of the Python pattern
//
//	(?<!\w)FIELD\s*:\s*("(?:\\.|[^"\\])*"|-?\d+)(?=\s*[,}])
//
// RE2 has no lookaround, so the lookbehind becomes the consuming alternation
// (?:^|[^\w]) and the lookahead becomes a consuming \s*[,}]. The captured value
// is identical; the extra consumed characters are discarded.
func callbackFieldRe(field string) *regexp.Regexp {
	return callbackFieldReCache(field)
}

// callbackParams parses the AJAX parameters embedded in the WFU callback page.
// It is a direct port of web_login_step.callback_params.
func callbackParams(body string) ([]pair, error) {
	m := reCallbackURL.FindStringSubmatch(body)
	if m == nil {
		return nil, inputErr("当前响应不是已确认的 WFU 登录回调模板")
	}
	var target string
	if err := json.Unmarshal([]byte(m[1]), &target); err != nil || target != "/OAuth2/wfu/login" {
		return nil, inputErr("当前响应不是已确认的 WFU 登录回调模板")
	}
	block := reDataBlock.FindStringSubmatch(body)
	if block == nil {
		return nil, inputErr("未找到回调 AJAX 参数块")
	}
	// The original appends '}' so the final field's trailing lookahead matches.
	haystack := block[1] + "}"

	pairs := make([]pair, 0, len(callbackFields))
	for _, field := range callbackFields {
		fm := callbackFieldRe(field).FindStringSubmatch(haystack)
		if fm == nil {
			return nil, inputErr("回调参数缺失: " + field)
		}
		raw := fm[1]
		if strings.HasPrefix(raw, `"`) {
			var value string
			if err := json.Unmarshal([]byte(raw), &value); err != nil {
				return nil, inputErr("回调参数无法解析: " + field)
			}
			pairs = append(pairs, pair{Key: field, Value: value})
			continue
		}
		// Numeric literal: requests stringifies it for form encoding.
		pairs = append(pairs, pair{Key: field, Value: raw})
	}
	for _, p := range pairs {
		if strings.Contains(p.Value, "[REDACTED]") {
			return nil, inputErr("不能使用脱敏文件，请保留原始回调响应")
		}
	}
	return pairs, nil
}

const webLoginEndpoint = "https://tyrzfw.chaoxing.com/OAuth2/wfu/login"

// completeWebLogin performs the single AJAX login step found in the official
// callback page. It uses the response already held in memory and never replays
// an authorization code.
func (l *QRLogin) completeWebLogin(ctx context.Context) (map[string]any, error) {
	if l.webLoginAttempted {
		if l.webLoginResult != nil {
			return l.webLoginResult, nil
		}
		return map[string]any{"state": "web_login_already_attempted"}, nil
	}
	resp := l.LastResponse
	if resp == nil || resp.StatusCode != 200 {
		return nil, inputErr("需要成功加载的原始回调响应")
	}
	if resp.URL == nil || resp.URL.Scheme != "https" ||
		strings.ToLower(resp.URL.Hostname()) != "tyrzfw.chaoxing.com" || resp.URL.Path != "/OAuth2/wfu/index" {
		return nil, inputErr("当前响应不是 WFU 官方回调页")
	}
	pairs, err := callbackParams(resp.Text())
	if err != nil {
		return nil, err
	}

	l.webLoginAttempted = true
	l.webLoginResult = map[string]any{
		"state": "web_login_attempted_no_result",
		"note":  "不自动重试，保留会话检查结果",
	}

	headers := http.Header{}
	headers.Set("Referer", resp.URL.String())
	headers.Set("X-Requested-With", "XMLHttpRequest")
	headers.Set("Accept", "application/json, text/javascript, */*; q=0.01")

	r, err := l.Session.do(ctx, http.MethodGet, mergeQueryString(webLoginEndpoint, encodePairs(pairs)), nil, headers)
	if err != nil {
		return nil, opWrap(err, "网页登录请求失败")
	}
	l.LastResponse = r

	var result map[string]any
	if r.StatusCode != 200 {
		result = map[string]any{
			"state":      "web_login_http_error",
			"diagnostic": l.ResponseDiagnostic(r),
		}
	} else {
		var payload any
		if err := json.Unmarshal(r.Body, &payload); err != nil {
			result = map[string]any{
				"state":        "web_login_non_json",
				"content_type": r.Header.Get("Content-Type"),
			}
		} else {
			l.webLoginPayload = payload
			keys := []string{}
			accepted := false
			if obj, ok := payload.(map[string]any); ok {
				for k := range obj {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				accepted = statusAccepted(obj["status"])
			}
			state := "web_login_rejected"
			if accepted {
				state = "web_login_confirmed"
			}
			result = map[string]any{
				"state":         state,
				"response_keys": keys,
				"cookie_names":  l.Session.CookieNames("https://tyrzfw.chaoxing.com/"),
				"token_names":   sortedKeys(l.Tokens),
				"next_page":     "https://i.chaoxing.com",
				"note":          "这里只确认网页登录接口状态；座位应用 token 仍需后续应用入口验证。",
			}
		}
	}
	l.webLoginResult = result
	return result, nil
}

// statusAccepted mirrors Python's `accepted is True or accepted == 1`.
func statusAccepted(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t == 1
	}
	return false
}
