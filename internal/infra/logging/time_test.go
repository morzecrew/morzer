package logging_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/morzecrew/morzer/internal/infra/logging"
)

// A record that carries its time carries it in UTC, as the lines stamped beside
// it in a Mac job's log do, on a machine set to any zone; one that does not
// carries none.
func TestARecordsTimeIsInUTC(t *testing.T) {
	local := time.Local
	time.Local = time.FixedZone("MSK", 3*3600)
	defer func() { time.Local = local }()

	for _, addTime := range []bool{true, false} {
		var out bytes.Buffer
		logger, _ := logging.New(logging.Options{Format: logging.FormatJSON, Writer: &out, AddTime: addTime})
		logger.Info("applied")

		var record map[string]any
		if err := json.Unmarshal(out.Bytes(), &record); err != nil {
			t.Fatalf("not one JSON record: %q: %v", out.String(), err)
		}
		stamp, has := record["time"].(string)
		switch {
		case !addTime && has:
			t.Errorf("a record without AddTime carries a time: %q", out.String())
		case addTime && !strings.HasSuffix(stamp, "Z"):
			t.Errorf("a record's time is %q, not in UTC", stamp)
		}
	}
}
