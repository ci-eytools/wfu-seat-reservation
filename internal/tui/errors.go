package tui

import (
	"context"
	"errors"

	"wfuseat/internal/chaoxing"
)

type errorKind string

const (
	kindNone        errorKind = ""
	kindPolicy      errorKind = "policy"
	kindSession     errorKind = "session"
	kindInput       errorKind = "input"
	kindUnsupported errorKind = "unsupported"
	kindNetwork     errorKind = "network"
	kindCanceled    errorKind = "canceled"
	kindUnknown     errorKind = "unknown"
)

// AppError is a classified, display-ready error. The main UI shows Short; the
// diagnostics view shows Detail. A raw error dump never reaches the main UI.
type AppError struct {
	Kind   errorKind
	Short  string
	Detail string
	Hint   string
}

func (e *AppError) Error() string { return e.Short }

// Recoverable reports whether suggesting a retry makes sense.
func (e *AppError) Recoverable() bool {
	return e.Kind != kindCanceled && e.Kind != kindNone
}

func (e *AppError) severity() severity {
	switch e.Kind {
	case kindPolicy, kindSession:
		return sevWarn
	case kindInput, kindUnsupported:
		return sevWarn
	case kindNetwork, kindUnknown:
		return sevErr
	case kindCanceled:
		return sevInfo
	default:
		return sevNone
	}
}

// classify maps any error onto the small set of states the UI renders. The
// order matters: a refused request is reported as a policy decision even when it
// travelled through the transport, and a cancellation is never shown as a
// failure.
func classify(err error) *AppError {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return &AppError{Kind: kindCanceled, Short: "操作已取消", Detail: err.Error()}

	case chaoxing.IsBlocked(err):
		return &AppError{
			Kind:   kindPolicy,
			Short:  "只读策略已拦截该请求",
			Detail: chaoxing.Message(err),
			Hint:   "本项目不发送预约请求，拦截是预期行为。原始记录见 d 诊断视图。",
		}

	case chaoxing.IsSessionExpired(err):
		return &AppError{
			Kind:   kindSession,
			Short:  "登录会话不可用",
			Detail: chaoxing.Message(err),
			Hint:   "按 r 重新扫码登录（登录面板在右侧主区域）。",
		}

	case chaoxing.IsUnsupported(err):
		return &AppError{
			Kind:   kindUnsupported,
			Short:  chaoxing.Message(err),
			Detail: err.Error(),
			Hint:   "该房间模式尚未适配，请改选其他房间。",
		}

	case chaoxing.IsInput(err):
		return &AppError{Kind: kindInput, Short: chaoxing.Message(err), Detail: err.Error()}

	case errors.Is(err, context.DeadlineExceeded):
		return &AppError{
			Kind:   kindNetwork,
			Short:  "请求超时",
			Detail: err.Error(),
			Hint:   "检查地址和网络；若已开启 VPN、TUN 或系统代理，请尝试关闭后按 r 重试。",
		}

	case chaoxing.IsNetwork(err):
		return &AppError{
			Kind:   kindNetwork,
			Short:  "无法连接服务",
			Detail: err.Error(),
			Hint:   "检查地址和网络；若已开启 VPN、TUN 或系统代理，请尝试关闭后按 r 重试。",
		}

	case chaoxing.IsOperational(err):
		return &AppError{
			Kind:   kindUnknown,
			Short:  chaoxing.Message(err),
			Detail: err.Error(),
			Hint:   "按 r 重试；详细信息见 d 诊断视图。",
		}
	}
	return &AppError{Kind: kindUnknown, Short: err.Error(), Detail: err.Error()}
}
