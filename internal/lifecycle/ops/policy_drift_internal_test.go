package ops

import (
	"reflect"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
)

// The drift check compares `installation.yaml` against the recorded state and
// tells an operator when a hand-edit did nothing. Its Policy comparison is a
// hand-written list of fields, and a field missing from that list is reported
// as "in step with the recorded state" while the file plainly disagrees --
// which is the one answer the check must never give wrongly.
//
// Two fields were already missing when this test was written: `BackupSchedule`,
// which is the most hand-editable line in the whole file, and
// `SkipScheduledBackups` beside it. Both had shipped without anybody noticing,
// because nothing failed.
//
// So this walks the struct instead of trusting the list. For each field it
// makes two policies that differ in that field alone and requires policyEqual
// to say so.
func TestPolicyDriftComparesEveryField(t *testing.T) {
	rt := reflect.TypeOf(domain.Policy{})
	for i := range rt.NumField() {
		field := rt.Field(i)
		t.Run(field.Name, func(t *testing.T) {
			a := domain.Policy{}
			b := domain.Policy{}
			differ := reflect.ValueOf(&b).Elem().Field(i)

			// A value distinguishable from the zero one, per kind.
			// Anything this cannot vary is a field the test would
			// pass on vacuously, so it fails instead.
			switch differ.Kind() {
			case reflect.Bool:
				differ.SetBool(true)
			case reflect.Int, reflect.Int64:
				differ.SetInt(7)
			case reflect.String:
				differ.SetString("changed")
			case reflect.Slice:
				// Built from the field's own element type rather
				// than assumed to be []string, so a slice of
				// something else fails with this message instead
				// of a reflect panic three frames down.
				if differ.Type().Elem().Kind() != reflect.String {
					t.Fatalf("Policy.%s is a slice of %s, which this test cannot vary",
						field.Name, differ.Type().Elem().Kind())
				}
				differ.Set(reflect.ValueOf([]string{"changed"}))
			default:
				t.Fatalf("Policy.%s is a %s, which this test cannot vary: "+
					"give it a case, or policyEqual is untested for it",
					field.Name, differ.Kind())
			}

			if policyEqual(a, b) {
				t.Errorf("policyEqual ignores Policy.%s, so a hand-edit of "+
					"`%s` is reported as in step with the recorded state",
					field.Name, field.Tag.Get("yaml"))
			}
		})
	}
}
