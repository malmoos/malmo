package updatetarget

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Window is the daily stretch of **local** time in which an update may start
// (UPDATES.md # 4 # Update window: "03:00–04:00 local by default, configurable,
// advanced setting"). Start and End are offsets from local midnight.
//
// Local, not UTC, because the whole point of the window is that it is the middle
// of the night where the box is.
type Window struct {
	Start time.Duration
	End   time.Duration
}

// DefaultWindow is 03:00–04:00 local.
var DefaultWindow = Window{Start: 3 * time.Hour, End: 4 * time.Hour}

// ParseWindow reads "HH:MM-HH:MM". An empty string is DefaultWindow.
//
// A window whose end is at or before its start wraps past midnight
// ("23:30-00:30"), which is the only reading that is not an error and is what a
// box configured that way plainly means.
func ParseWindow(s string) (Window, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultWindow, nil
	}
	start, end, ok := strings.Cut(s, "-")
	if !ok {
		return Window{}, fmt.Errorf("updatetarget: window %q is not HH:MM-HH:MM", s)
	}
	a, err := parseClock(start)
	if err != nil {
		return Window{}, err
	}
	b, err := parseClock(end)
	if err != nil {
		return Window{}, err
	}
	if a == b {
		return Window{}, fmt.Errorf("updatetarget: window %q is empty", s)
	}
	return Window{Start: a, End: b}, nil
}

func parseClock(s string) (time.Duration, error) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return 0, fmt.Errorf("updatetarget: %q is not HH:MM", s)
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("updatetarget: %q is not a time of day", s)
	}
	return time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute, nil
}

func (w Window) String() string {
	return clock(w.Start) + "-" + clock(w.End)
}

func clock(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d.Hours()), int(d.Minutes())%60)
}

// Contains reports whether t (in its own location) falls inside the window.
func (w Window) Contains(t time.Time) bool {
	d := timeOfDay(t)
	if w.Start < w.End {
		return d >= w.Start && d < w.End
	}
	// Wraps past midnight.
	return d >= w.Start || d < w.End
}

// Occurrence names the window t falls in, as the local date-time the window
// opened. It is how the loop remembers "I already tried tonight" without
// remembering a clock reading: two attempts in one window share an occurrence,
// and tomorrow's window does not.
//
// Only meaningful when Contains(t) is true.
func (w Window) Occurrence(t time.Time) time.Time {
	midnight := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	start := midnight.Add(w.Start)
	if start.After(t) {
		// A wrapping window that opened yesterday.
		start = start.AddDate(0, 0, -1)
	}
	return start
}

// timeOfDay is how far into the local day t is. Built from the wall-clock
// fields rather than by subtracting midnight, so a DST jump shifts the window
// with the clock instead of sliding it by an hour.
func timeOfDay(t time.Time) time.Duration {
	h, m, s := t.Clock()
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second
}
