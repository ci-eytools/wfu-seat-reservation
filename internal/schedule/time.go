package schedule

import (
	"errors"
	"github.com/robfig/cron/v3"
	"strings"
	"time"
)

var Zone = time.FixedZone("Asia/Shanghai", 8*3600)

func Parse(spec string) (cron.Schedule, error) {
	fields := strings.Fields(spec)
	if len(fields) != 5 && len(fields) != 6 {
		return nil, errors.New("cron 需要五字段（分 时 日 月 周）或六字段（秒 分 时 日 月 周）")
	}
	return cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse("CRON_TZ=Asia/Shanghai " + spec)
}
func Next(spec, clock string, after time.Time) (time.Time, error) {
	if spec != "" {
		s, err := Parse(spec)
		if err != nil {
			return time.Time{}, err
		}
		n := s.Next(after.In(Zone))
		if n.IsZero() {
			return n, errors.New("cron 在未来五年内没有匹配时间")
		}
		return n, nil
	}
	t, err := time.Parse("15:04:05", clock)
	if err != nil {
		return time.Time{}, errors.New("执行时间需要 HH:MM:SS")
	}
	a := after.In(Zone)
	n := time.Date(a.Year(), a.Month(), a.Day(), t.Hour(), t.Minute(), t.Second(), 0, Zone)
	if !n.After(a) {
		n = n.AddDate(0, 0, 1)
	}
	return n, nil
}
func Offset(day string, at time.Time) (int, error) {
	d, err := time.ParseInLocation("2006-01-02", day, Zone)
	if err != nil {
		return 0, err
	}
	a := at.In(Zone)
	base := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, Zone)
	return int(d.Sub(base).Hours() / 24), nil
}
