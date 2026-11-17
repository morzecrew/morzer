package launchd

import (
	"context"
	"testing"

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
