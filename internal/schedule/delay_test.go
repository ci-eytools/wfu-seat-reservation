package schedule

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFixedDelayExactValueAndCancellation(t *testing.T) {
	for _, max := range []int{0, 1, 50, 60000} {
		for i := 0; i < 32; i++ {
			n, e := fixedDelayMS(max)
			if e != nil || n != max {
				t.Fatal(max, n, e)
			}
		}
	}
	for _, bad := range []int{-1, 60001} {
		if _, e := fixedDelayMS(bad); e == nil {
			t.Fatal("invalid bound accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if e := waitDelay(ctx, 60000); !errors.Is(e, context.Canceled) || time.Since(start) > time.Second {
		t.Fatal(e)
	}
}
