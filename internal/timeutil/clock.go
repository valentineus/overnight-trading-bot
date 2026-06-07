package timeutil

import (
	"fmt"
	"time"
)

type Clock interface {
	Now() time.Time
	Sleep(ctxDone <-chan struct{}, d time.Duration) bool
}

type RealClock struct {
	Loc *time.Location
}

func (c RealClock) Now() time.Time {
	now := time.Now()
	if c.Loc != nil {
		return now.In(c.Loc)
	}
	return now
}

func (c RealClock) Sleep(ctxDone <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctxDone:
		return false
	}
}

type TimeOfDay struct {
	Duration time.Duration
}

func ParseTimeOfDay(raw string) (TimeOfDay, error) {
	parsed, err := time.Parse("15:04:05", raw)
	if err != nil {
		return TimeOfDay{}, fmt.Errorf("parse time of day %q: %w", raw, err)
	}
	return TimeOfDay{
		Duration: time.Duration(parsed.Hour())*time.Hour +
			time.Duration(parsed.Minute())*time.Minute +
			time.Duration(parsed.Second())*time.Second,
	}, nil
}

func (t *TimeOfDay) UnmarshalText(text []byte) error {
	parsed, err := ParseTimeOfDay(string(text))
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

func (t TimeOfDay) String() string {
	total := int64(t.Duration.Seconds())
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func (t TimeOfDay) On(date time.Time, loc *time.Location) time.Time {
	local := date.In(loc)
	y, m, d := local.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, loc)
	return midnight.Add(t.Duration)
}

type Window struct {
	Start TimeOfDay
	End   TimeOfDay
}

func (w Window) Contains(now time.Time, loc *time.Location) bool {
	start := w.Start.On(now, loc)
	end := w.End.On(now, loc)
	return !now.Before(start) && now.Before(end)
}

func Drift(local, server time.Time) time.Duration {
	d := local.Sub(server)
	if d < 0 {
		return -d
	}
	return d
}
