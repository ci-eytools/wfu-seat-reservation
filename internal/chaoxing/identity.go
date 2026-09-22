package chaoxing

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

var loginInfoRE = regexp.MustCompile(`(?s)\bvar\s+userLoginInfo\s*=\s*(\{.*?\})\s*\|\|`)
var studentRE = regexp.MustCompile(`^[0-9]{1,44}$`)

func AccountNameFromPage(page string) string {
	m := loginInfoRE.FindStringSubmatch(page)
	if len(m) < 2 {
		return ""
	}
	var v struct {
		UserInfo struct {
			Name    string `json:"uname"`
			Student string `json:"sno"`
		} `json:"userInfo"`
	}
	if json.Unmarshal([]byte(m[1]), &v) != nil || !studentRE.MatchString(v.UserInfo.Student) {
		return ""
	}
	name := strings.TrimSpace(v.UserInfo.Name)
	if name == "" {
		return ""
	}
	surname := ""
	for _, s := range strings.Fields("欧阳 司马 上官 诸葛 东方 皇甫 尉迟 公孙 慕容 司徒 司空 夏侯 令狐 长孙 宇文 独孤 南宫 万俟 闻人 申屠 公羊 澹台 仲孙 轩辕 钟离 濮阳 太叔 宗政 淳于 单于 太史 端木 颛孙 梁丘 左丘 东郭 呼延 羊舌 微生") {
		if strings.HasPrefix(name, s) {
			surname = s
			break
		}
	}
	if surname == "" {
		_, n := utf8.DecodeRuneInString(name)
		surname = name[:n]
	}
	return surname + v.UserInfo.Student
}
func (c *SeatClient) AccountName() string {
	if c.Remote != nil && c.remoteView != nil {
		return c.remoteView.DisplayName
	}
	if c.Login == nil || c.Login.SeatPageResponse == nil {
		return ""
	}
	return AccountNameFromPage(c.Login.SeatPageResponse.Text())
}

// StudentID is read only from the authenticated seat home response.
func (c *SeatClient) StudentID() string {
	if c.Login == nil || c.Login.SeatPageResponse == nil {
		return ""
	}
	m := loginInfoRE.FindStringSubmatch(c.Login.SeatPageResponse.Text())
	if len(m) < 2 {
		return ""
	}
	var v struct {
		UserInfo struct {
			Student string `json:"sno"`
		} `json:"userInfo"`
	}
	if json.Unmarshal([]byte(m[1]), &v) != nil || !studentRE.MatchString(v.UserInfo.Student) {
		return ""
	}
	return v.UserInfo.Student
}
