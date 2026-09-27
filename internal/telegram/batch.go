package telegram

import (
	"fmt"
	"strings"
	"time"
)

type BatchItem struct {
	Account string
	Item    Item
}

func RenderBatch(at time.Time, cfg Settings, items []BatchItem) []string {
	const perPage = 6
	if len(items) == 0 {
		return nil
	}
	pages := (len(items) + perPage - 1) / perPage
	good, bad, unknown := 0, 0, 0
	accounts := map[string]bool{}
	for _, v := range items {
		accounts[v.Account] = true
		switch v.Item.State {
		case "succeeded":
			good++
		case "failed", "missed", "paused", "needs_login", "not_run":
			bad++
		default:
			unknown++
		}
	}
	name := cfg.Nickname
	if name == "" {
		name = "notice"
	}
	title, footer := "🌙 座位来信", "今天的努力已经记下，接下来，把时间留给自己。🌿"
	if good == len(items) {
		title = "🎉 座位就绪"
		footer = "书、耳机和好心情，明天记得一起带上。☀️"
	}
	if good == 0 {
		title = "📬 预约进度已送达"
		footer = "这次没等到理想的位置，也别打乱自己的节奏。🌱"
	}
	if unknown > 0 {
		footer = "有结果仍待核实，请先查看学校记录，再决定下一步。🔎"
	}
	result := make([]string, 0, pages)
	for page := 0; page < pages; page++ {
		var b strings.Builder
		fmt.Fprintf(&b, "<b>%s</b> · %s\n<code>%s</code> 批次 · 北京时间\n\n%s，预约结果整理好了。\n👥 %d 个账号 · %d 项预约\n✅ 成功 %d  ｜  🟠 未成功 %d  ｜  ❔ 待核实 %d\n", title, esc(name, 32), at.Format("01-02 15:04"), esc(name, 32), len(accounts), len(items), good, bad, unknown)
		end := (page + 1) * perPage
		if end > len(items) {
			end = len(items)
		}
		for _, row := range items[page*perPage : end] {
			v := row.Item
			state := "❔ 结果未知"
			switch v.State {
			case "succeeded":
				state = "✅ 预约成功"
			case "failed":
				state = "🟠 未预约成功"
			case "missed":
				state = "⏳ 错过执行时间"
			case "needs_login":
				state = "🔑 登录失效，请重新扫码"
			case "paused":
				state = "⏸ 已暂停"
			case "not_run":
				state = "⏳ 截至汇总时尚未执行"
			case "running":
				state = "⌛ 仍在执行，请稍后核实"
			}
			fmt.Fprintf(&b, "\n<b>%s</b>  ·  %s\n📍 %s\n🗓 %s  <code>%s–%s</code>\n🪑 候选 %s\n", esc(row.Account, 24), state, esc(v.Room, 40), esc(v.Day, 10), esc(v.Start, 5), esc(v.End, 5), esc(strings.Join(v.Seats, " → "), 32))
			if v.State != "succeeded" && v.Detail != "" {
				fmt.Fprintf(&b, "原因：%s\n", esc(v.Detail, 120))
			}
		}
		fmt.Fprintf(&b, "\n%s\n<i>第 %d/%d 页 · 以学校预约记录为准</i>", footer, page+1, pages)
		result = append(result, b.String())
	}
	return result
}

// Message carries a compact report and inline controls.
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}
type Message struct {
	Text     string     `json:"text"`
	Keyboard [][]Button `json:"keyboard,omitempty"`
}

func CompactBatch(items []BatchItem) []string {
	good := 0
	failures := []string{}
	for _, row := range items {
		v := row.Item
		if v.State == "succeeded" {
			good++
			continue
		}
		reason := v.Detail
		if reason == "" {
			switch v.State {
			case "needs_login":
				reason = "登录失效，请重新扫码"
			case "missed":
				reason = "错过执行时间"
			case "paused":
				reason = "任务已暂停"
			case "not_run":
				reason = "尚未执行，请检查调度"
			case "unknown", "running":
				reason = "结果待核实，请查看学校记录"
			default:
				reason = "预约未成功，请查看详情"
			}
		}
		failures = append(failures, fmt.Sprintf("• %s：%s", esc(row.Account, 24), esc(reason, 180)))
	}
	// Failure details are paginated rather than silently discarded.
	pages := max(1, (len(failures)+5)/6)
	out := make([]string, 0, pages)
	for i := 0; i < pages; i++ {
		text := fmt.Sprintf("<b>成功/全部：%d/%d</b>\n\n<b>失败：</b>", good, len(items))
		if len(failures) == 0 {
			text += "无 🎉"
		} else {
			end := min((i+1)*6, len(failures))
			text += "\n" + strings.Join(failures[i*6:end], "\n")
		}
		if pages > 1 {
			text += fmt.Sprintf("\n\n<i>第 %d/%d 页</i>", i+1, pages)
		}
		out = append(out, text)
	}
	return out
}
