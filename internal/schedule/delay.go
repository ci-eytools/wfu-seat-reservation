package schedule

import (
	"context"
	"errors"
	"time"
)

func fixedDelayMS(ms int) (int, error) {
	if ms < 0 || ms > 60000 {
		return 0, errors.New("固定延迟需要 0–60000 毫秒")
	}
	return ms, nil
}
func waitDelay(ctx context.Context, ms int) error {
	if ms == 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
