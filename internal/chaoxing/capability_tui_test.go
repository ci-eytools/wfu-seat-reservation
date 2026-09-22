//go:build wfuseat_tui

package chaoxing

import (
	"context"
	"net/http"
	"testing"
)

type thinForbiddenTransport struct{ t *testing.T }

func (f thinForbiddenTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Fatal("thin client reached the school network")
	return nil, nil
}
func TestThinClientBlocksAllSchoolTraffic(t *testing.T) {
	guard := NewGuardedTransport(thinForbiddenTransport{t})
	for _, raw := range []string{"https://e.wfu.edu.cn/ssoApi/appQRCode", "https://office.chaoxing.com/front/third/apps/seat/index"} {
		req, e := http.NewRequest("GET", raw, nil)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = guard.RoundTrip(req); !IsBlocked(e) {
			t.Fatalf("request not blocked: %v", e)
		}
	}
	client := NewSeatClient(nil, "", "")
	if _, e := client.SubmitReservation(context.Background()); !IsBlocked(e) {
		t.Fatalf("submit not blocked: %v", e)
	}
}
