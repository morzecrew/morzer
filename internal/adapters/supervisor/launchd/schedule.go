package launchd

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

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

// refuseSchedule is the error `config set` reports for a schedule launchd
// cannot run as written.
func refuseSchedule(expr string, why error) error {
	return domain.ValidationError(nil, "launchd cannot run the schedule %q as written: %s", expr, why).
		WithHint("a Mac runs a schedule at fixed times: `*-*-* 02:30` daily, `Sun *-*-* 04:00` " +
			"weekly, `*-*-01 03:00` monthly, or `*:15` each hour; not a repetition, " +
			"a range of times, seconds or a time zone")
}
