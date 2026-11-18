package launchd

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
)

// interval is one entry of a plist's StartCalendarInterval: the fields launchd
// matches, all of them, each minute. An empty one matches every minute.
type interval map[string]int

// The names launchd gives the fields, in the order a plist lists them.
const (
	keyMonth   = "Month"
	keyDay     = "Day"
	keyWeekday = "Weekday"
	keyHour    = "Hour"
	keyMinute  = "Minute"
)

var intervalKeys = []string{keyMonth, keyDay, keyWeekday, keyHour, keyMinute}

// shorthands are systemd's names for a calendar, each of which launchd can run
// (systemd.time(7)).
var shorthands = map[string]string{
	"minutely": "*-*-* *:*:00",
	"hourly":   "*-*-* *:00:00",
	"daily":    "*-*-* 00:00:00",
	"weekly":   "Mon *-*-* 00:00:00",
	"monthly":  "*-*-01 00:00:00",
	"yearly":   "*-01-01 00:00:00",
}

// firstOfMonths are the two of systemd's names that list months, which the
// grammar below does not: midnight on the first of each.
var firstOfMonths = map[string][]int{
	"quarterly":    {1, 4, 7, 10},
	"semiannually": {1, 7},
}

// weekdays are launchd's numbers for systemd's day names, Sunday 0.
var weekdays = map[string]int{
	"sun": 0, "sunday": 0, "mon": 1, "monday": 1, "tue": 2, "tuesday": 2,
	"wed": 3, "wednesday": 3, "thu": 4, "thursday": 4, "fri": 5, "friday": 5,
	"sat": 6, "saturday": 6,
}

// calendar translates an OnCalendar expression into the intervals that run it
// at exactly the same times, or says why there are none (RFC 0055 §5.6).
//
// The stored schedule keeps systemd's grammar on every platform, so an
// installation reads the same wherever it runs, and a Mac takes the part of it
// launchd can say: fixed minutes, hours, weekdays, days and months. Anything
// else -- a repetition, a range of times, seconds, a year, a time zone -- would
// run at other times than it names, and is refused at `config set` rather than
// rounded to something nobody asked for.
func calendar(expr string) ([]interval, error) {
	fields := strings.Fields(expr)
	if len(fields) == 1 {
		if long, ok := shorthands[strings.ToLower(fields[0])]; ok {
			fields = strings.Fields(long)
		}
		if months, ok := firstOfMonths[strings.ToLower(fields[0])]; ok {
			out := make([]interval, 0, len(months))
			for _, m := range months {
				out = append(out, interval{keyMonth: m, keyDay: 1, keyHour: 0, keyMinute: 0})
			}
			return out, nil
		}
	}
	var days []int
	if len(fields) > 1 && strings.IndexFunc(fields[0], isLetter) == 0 {
		var err error
		if days, err = weekdayList(fields[0]); err != nil {
			return nil, err
		}
		fields = fields[1:]
	}
	month, day := -1, -1
	if len(fields) == 2 {
		var err error
		if month, day, err = date(fields[0]); err != nil {
			return nil, err
		}
		fields = fields[1:]
	}
	if len(fields) != 1 {
		return nil, fmt.Errorf("it is not a weekday, a date and a time")
	}
	hour, minute, err := clock(fields[0])
	if err != nil {
		return nil, err
	}
	// launchd starts a job whose Day *or* Weekday matches, as cron does, and
	// systemd runs one only when both do.
	if days != nil && day >= 0 {
		return nil, fmt.Errorf("launchd runs a weekday and a day of the month on either, not on both")
	}

	base := interval{}
	for key, value := range map[string]int{keyMonth: month, keyDay: day, keyHour: hour, keyMinute: minute} {
		if value >= 0 {
			base[key] = value
		}
	}
	if days == nil {
		return []interval{base}, nil
	}
	out := make([]interval, 0, len(days))
	for _, d := range days {
		entry := interval{keyWeekday: d}
		for k, v := range base {
			entry[k] = v
		}
		out = append(out, entry)
	}
	return out, nil
}

func isLetter(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }

// weekdayList reads `Mon`, `Mon,Wed` or `Mon..Fri`, in launchd's numbers.
func weekdayList(text string) ([]int, error) {
	var out []int
	for item := range strings.SplitSeq(text, ",") {
		from, to, isRange := strings.Cut(item, "..")
		first, ok := weekdays[strings.ToLower(from)]
		if !ok {
			return nil, fmt.Errorf("%q is not a weekday", from)
		}
		last := first
		if isRange {
			if last, ok = weekdays[strings.ToLower(to)]; !ok {
				return nil, fmt.Errorf("%q is not a weekday", to)
			}
			if last < first {
				return nil, fmt.Errorf("%s runs past Saturday", item)
			}
		}
		for d := first; d <= last; d++ {
			if !slices.Contains(out, d) {
				out = append(out, d)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// date reads `*-*-*`, `*-*-01` or `*-06-*`: launchd has no year, so the year is
// every year.
func date(text string) (month, day int, err error) {
	parts := strings.Split(text, "-")
	if len(parts) != 3 {
		return 0, 0, fmt.Errorf("%q is not a date", text)
	}
	if parts[0] != "*" {
		return 0, 0, fmt.Errorf("launchd has no year")
	}
	if month, err = field(parts[1], 1, 12, "month"); err != nil {
		return 0, 0, err
	}
	if day, err = field(parts[2], 1, 31, "day"); err != nil {
		return 0, 0, err
	}
	return month, day, nil
}

// clock reads `02:30`, `02:30:00` or `*:15`. launchd starts a job at a minute,
// so the seconds are none.
func clock(text string) (hour, minute int, err error) {
	parts := strings.Split(text, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, fmt.Errorf("%q is not a time", text)
	}
	if len(parts) == 3 && parts[2] != "00" && parts[2] != "0" {
		return 0, 0, fmt.Errorf("launchd starts a job at a minute, so the seconds must be 00")
	}
	if hour, err = field(parts[0], 0, 23, "hour"); err != nil {
		return 0, 0, err
	}
	if minute, err = field(parts[1], 0, 59, "minute"); err != nil {
		return 0, 0, err
	}
	if hour >= 0 && minute < 0 {
		return 0, 0, fmt.Errorf("every minute of an hour is sixty schedules")
	}
	return hour, minute, nil
}

// field is a number in [lo, hi], or -1 for `*`.
func field(text string, lo, hi int, what string) (int, error) {
	if text == "*" {
		return -1, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || strings.ContainsAny(text, "+-") || n < lo || n > hi {
		return 0, fmt.Errorf("%q is not one %s", text, what)
	}
	return n, nil
}

// latest is the last minute at or before now at which intervals start a job,
// in now's location, which is the one launchd reads them in. Zero when none did
// in the last eight years, as far back as a leap day's schedule needs: 2100 has
// none.
func latest(intervals []interval, now time.Time) time.Time {
	var out time.Time
	for _, i := range intervals {
		if t := i.latest(now); t.After(out) {
			out = t
		}
	}
	return out
}

func (i interval) latest(now time.Time) time.Time {
	loc := now.Location()
	y, m, d := now.Date()
	for back := 0; back <= 8*366; back++ {
		// Noon, so a clock change at midnight cannot move it to another day.
		day := time.Date(y, m, d-back, 12, 0, 0, 0, loc)
		if !i.on(day) {
			continue
		}
		for h := 23; h >= 0; h-- {
			if v, ok := i[keyHour]; ok && v != h {
				continue
			}
			for minute := 59; minute >= 0; minute-- {
				if v, ok := i[keyMinute]; ok && v != minute {
					continue
				}
				if t, ok := wallClock(day, h, minute, now); ok {
					return t
				}
			}
		}
	}
	return time.Time{}
}

// wallClock is the latest instant at or before now that day's clock read
// h:minute on day: none when a clock change skipped that time, and the later of
// the two at or before now when one repeated it.
func wallClock(day time.Time, h, minute int, now time.Time) (time.Time, bool) {
	t := time.Date(day.Year(), day.Month(), day.Day(), h, minute, 0, 0, day.Location())
	var out time.Time
	for _, c := range []time.Time{t.Add(-time.Hour), t, t.Add(time.Hour)} {
		y, m, d := c.Date()
		if c.Hour() == h && c.Minute() == minute && y == day.Year() && m == day.Month() && d == day.Day() &&
			!c.After(now) && c.After(out) {
			out = c
		}
	}
	return out, !out.IsZero()
}

// on is whether day is one the interval's date fields match.
func (i interval) on(day time.Time) bool {
	matches := func(key string, value int) bool {
		v, ok := i[key]
		return !ok || v == value
	}
	return matches(keyMonth, int(day.Month())) && matches(keyDay, day.Day()) &&
		matches(keyWeekday, int(day.Weekday()))
}

// refuseSchedule is the error `config set` reports for a schedule launchd
// cannot run as written.
func refuseSchedule(expr string, why error) error {
	return domain.ValidationError(nil, "launchd cannot run the schedule %q as written: %s", expr, why).
		WithHint("a Mac runs a schedule at fixed times: `*-*-* 02:30` daily, `Sun *-*-* 04:00` " +
			"weekly, `*-*-01 03:00` monthly, or `*:15` each hour; not a repetition, " +
			"a range of times, seconds or a time zone")
}
