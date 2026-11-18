package launchd

import (
	"context"
	"testing"
	"time"
	_ "time/tzdata" // the clock change below, wherever the tests run

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/morzecrew/morzer/internal/domain"
)

// Every form launchd runs at exactly the times systemd would, and what each
// becomes (RFC 0055 §5.6).
func TestACalendarLaunchdRunsExactlyTranslates(t *testing.T) {
	for expr, want := range map[string][]interval{
		"*-*-* 02:30:00":      {{keyHour: 2, keyMinute: 30}},
		"*-*-* 02:30":         {{keyHour: 2, keyMinute: 30}},
		"02:30":               {{keyHour: 2, keyMinute: 30}},
		"*-*-* *:00:00":       {{keyMinute: 0}},
		"*:15":                {{keyMinute: 15}},
		"*:*:00":              {{}},
		"Sun *-*-* 04:00:00":  {{keyWeekday: 0, keyHour: 4, keyMinute: 0}},
		"sunday 04:00":        {{keyWeekday: 0, keyHour: 4, keyMinute: 0}},
		"Mon,Wed *-*-* 09:00": {{keyWeekday: 1, keyHour: 9, keyMinute: 0}, {keyWeekday: 3, keyHour: 9, keyMinute: 0}},
		"Mon..Wed 09:00": {
			{keyWeekday: 1, keyHour: 9, keyMinute: 0}, {keyWeekday: 2, keyHour: 9, keyMinute: 0},
			{keyWeekday: 3, keyHour: 9, keyMinute: 0},
		},
		"*-*-01 03:00":  {{keyDay: 1, keyHour: 3, keyMinute: 0}},
		"*-06-* 03:00":  {{keyMonth: 6, keyHour: 3, keyMinute: 0}},
		"*-12-25 07:05": {{keyMonth: 12, keyDay: 25, keyHour: 7, keyMinute: 5}},
		"daily":         {{keyHour: 0, keyMinute: 0}},
		"hourly":        {{keyMinute: 0}},
		"weekly":        {{keyWeekday: 1, keyHour: 0, keyMinute: 0}},
		"monthly":       {{keyDay: 1, keyHour: 0, keyMinute: 0}},
		"yearly":        {{keyMonth: 1, keyDay: 1, keyHour: 0, keyMinute: 0}},
		"minutely":      {{}},
		"quarterly": {
			{keyMonth: 1, keyDay: 1, keyHour: 0, keyMinute: 0}, {keyMonth: 4, keyDay: 1, keyHour: 0, keyMinute: 0},
			{keyMonth: 7, keyDay: 1, keyHour: 0, keyMinute: 0}, {keyMonth: 10, keyDay: 1, keyHour: 0, keyMinute: 0},
		},
		"semiannually":   {{keyMonth: 1, keyDay: 1, keyHour: 0, keyMinute: 0}, {keyMonth: 7, keyDay: 1, keyHour: 0, keyMinute: 0}},
		"Mon,Mon 01:00":  {{keyWeekday: 1, keyHour: 1, keyMinute: 0}},
		"Fri,Mon 01:00":  {{keyWeekday: 1, keyHour: 1, keyMinute: 0}, {keyWeekday: 5, keyHour: 1, keyMinute: 0}},
		"*-*-* 23:59:00": {{keyHour: 23, keyMinute: 59}},
	} {
		t.Run(expr, func(t *testing.T) {
			got, err := calendar(expr)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

// The first form of each kind launchd cannot run as written, refused at
// `config set` rather than rounded.
func TestACalendarLaunchdCannotRunIsRefusedSayingWhy(t *testing.T) {
	for expr, why := range map[string]string{
		"*:0/5":                   `"0/5" is not one minute`,
		"*-*-* 02:30:15":          "the seconds must be 00",
		"2026-*-* 02:30":          "launchd has no year",
		"*-*-* 02:30 UTC":         "not a weekday, a date and a time",
		"*-*-* 02..04:00":         `"02..04" is not one hour`,
		"*-*-* 02,14:00":          `"02,14" is not one hour`,
		"*-*-* 02:*":              "every minute of an hour",
		"Mon *-*-01 04:00":        "on either, not on both",
		"Sat..Sun 09:00":          "runs past Saturday",
		"Someday 09:00":           `"Someday" is not a weekday`,
		"*-13-01 03:00":           `"13" is not one month`,
		"*-*-32 03:00":            `"32" is not one day`,
		"*-*-* 24:00":             `"24" is not one hour`,
		"*-*-* +3:00":             `"+3" is not one hour`,
		"*-*-1..7 03:00":          `"1..7" is not one day`,
		"*-*-*":                   "is not a time",
		"Mon 09:00 *-*-* 01:00":   "not a weekday, a date and a time",
		"*-*-* 02:30:00 Europe/X": "not a weekday, a date and a time",
	} {
		t.Run(expr, func(t *testing.T) {
			err := (&Supervisor{}).ValidateSchedule(context.Background(), expr)
			require.ErrorIs(t, err, domain.ErrValidation)
			assert.Contains(t, err.Error(), why)
		})
	}
}

// The catch-up compares a job's stamp with the last time its schedule passed,
// in the Mac's own time (RFC 0055 §5.6).
func TestTheLatestTimeAScheduleRanIsTheLastOneAtOrBeforeNow(t *testing.T) {
	now := time.Date(2026, time.November, 18, 10, 0, 0, 0, time.UTC) // a Wednesday
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, time.UTC) }
	for expr, want := range map[string]time.Time{
		"*-*-* 02:30":         at(2026, time.November, 18, 2, 30),
		"*-*-* 11:00":         at(2026, time.November, 17, 11, 0),
		"*-*-* 10:00":         now,
		"*:15":                at(2026, time.November, 18, 9, 15),
		"minutely":            now,
		"Sat,Sun *-*-* 04:00": at(2026, time.November, 15, 4, 0),
		"Wed 10:01":           at(2026, time.November, 11, 10, 1),
		"*-*-31 03:00":        at(2026, time.October, 31, 3, 0),
		"*-11-* 23:59":        at(2026, time.November, 17, 23, 59),
		"*-12-25 07:00":       at(2025, time.December, 25, 7, 0),
		"quarterly":           at(2026, time.October, 1, 0, 0),
		"*-02-29 00:00":       at(2024, time.February, 29, 0, 0),
	} {
		times, err := calendar(expr)
		require.NoError(t, err, expr)
		got := latest(times, now)
		assert.True(t, want.Equal(got), "%s: want %s, got %s", expr, want, got)
	}
}

// A time the clocks skipped did not happen that day, and a schedule runs in the
// Mac's own time zone.
func TestTheLatestTimeKeepsToTheMacsClock(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	times, err := calendar("*-*-* 02:30")
	require.NoError(t, err)

	// Clocks went from 02:00 to 03:00 on 29 March 2026.
	got := latest(times, time.Date(2026, time.March, 29, 12, 0, 0, 0, berlin))
	assert.True(t, time.Date(2026, time.March, 28, 2, 30, 0, 0, berlin).Equal(got), "got %s", got)

	got = latest(times, time.Date(2026, time.November, 18, 2, 0, 0, 0, berlin))
	assert.True(t, time.Date(2026, time.November, 17, 2, 30, 0, 0, berlin).Equal(got), "got %s", got)
	assert.True(t, time.Date(2026, time.November, 17, 1, 30, 0, 0, time.UTC).Equal(got), "got %s", got)
}
