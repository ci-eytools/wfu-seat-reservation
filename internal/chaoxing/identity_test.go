package chaoxing

import "testing"

func TestAccountNameFromAuthenticatedPage(t *testing.T) {
	for _, tt := range []struct{ page, want string }{
		{`var userLoginInfo = {"userInfo":{"uname":"张三","sno":"20260001"}}||{}`, "张20260001"},
		{`var userLoginInfo = {"userInfo":{"uname":"欧阳明","sno":"20260002"}} || {}`, "欧阳20260002"},
		{`var userLoginInfo = {"userInfo":{"uname":"张三"}}||{}`, ""},
		{`var userLoginInfo = {"userInfo":{"uname":"张三","sno":"../bad"}}||{}`, ""},
	} {
		if got := AccountNameFromPage(tt.page); got != tt.want {
			t.Fatalf("%q != %q", got, tt.want)
		}
	}
}
