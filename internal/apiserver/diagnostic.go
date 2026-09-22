package apiserver

import (
	"context"
	"errors"
	"net"
	"wfuseat/internal/chaoxing"
)

// qrFailure exposes classifications, never raw transport URLs, credentials or
// response bodies. Wrapped HTTP errors may contain proxy passwords.
func qrFailure(err error, proxy string) string {
	detail := "学校登录入口响应异常"
	var network net.Error
	var operational *chaoxing.OperationalError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		detail = "后端连接学校登录入口超时"
	case errors.As(err, &network):
		if network.Timeout() {
			detail = "后端连接学校登录入口超时"
		} else {
			detail = "后端无法连接学校登录入口"
		}
	case chaoxing.IsBlocked(err):
		detail = "后端构建禁用了学校网络访问，请使用完整版或独立后端"
	case errors.As(err, &operational) && operational.Err == nil:
		detail = operational.Msg
	}
	if proxy == "" {
		return "二维码获取失败：" + detail + "；后端当前为直连，需要代理时请在后端启动参数添加 --proxy http://127.0.0.1:7890"
	}
	return "二维码获取失败：" + detail + "；后端已配置代理，请检查后端主机上的代理连接"
}
