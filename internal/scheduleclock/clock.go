package scheduleclock

import (
	"errors"
	"time"
)

func Validate(s string) error {
	if _, err := time.Parse("15:04:05", s); err != nil {
		return errors.New("全局执行时间需要 HH:MM:SS")
	}
	return nil
}
