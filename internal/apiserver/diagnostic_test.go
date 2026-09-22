package apiserver

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"wfuseat/internal/chaoxing"
)

func TestQRFailureRetainsCauseWithoutLeakingURLs(t *testing.T) {
	err := &chaoxing.OperationalError{Msg: "无法获取二维码", Err: &url.Error{Op: "Get", URL: "https://user:secret@example.com?token=private", Err: context.DeadlineExceeded}}
	for _, proxy := range []string{"", "http://user:password@localhost:7890"} {
		got := qrFailure(err, proxy)
		if !strings.Contains(got, "超时") {
			t.Fatal(got)
		}
		for _, secret := range []string{"secret", "private", "password", "example.com"} {
			if strings.Contains(got, secret) {
				t.Fatal("sensitive transport detail leaked")
			}
		}
		if proxy == "" && !strings.Contains(got, "--proxy") {
			t.Fatal("missing server proxy guidance")
		}
	}
	got := qrFailure(&chaoxing.OperationalError{Msg: "无法获取二维码: HTTP 403"}, "")
	if !strings.Contains(got, "HTTP 403") {
		t.Fatal(got)
	}
}
