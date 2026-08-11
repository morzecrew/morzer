package logging_test

import (
	"io"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/infra/logging"
)

// The stream filter is what stands between a vendor's container logs and an
// operator's terminal. Its two interesting properties are both about where a
// read boundary falls, which is the kernel's business and not the caller's: a
// secret split across two reads must still be caught, and a line that never
// ends must be dropped rather than passed through unmatched.

// chunks is a reader that hands over exactly one arranged chunk per Read.
//
// Not strings.Reader: that fills whatever buffer it is given, so every line
// arrives whole and the case this filter exists for -- a value straddling two
// reads -- can never occur.
type chunks struct {
	parts []string
	at    int
}

func (c *chunks) Read(p []byte) (int, error) {
	if c.at >= len(c.parts) {
		return 0, io.EOF
	}
	part := c.parts[c.at]
	if len(part) > len(p) {
		n := copy(p, part)
		c.parts[c.at] = part[n:]
		return n, nil
	}
	c.at++
	return copy(p, part), nil
}

func (c *chunks) Close() error { return nil }

func filtered(t *testing.T, secrets []string, parts ...string) string {
	t.Helper()

	r := logging.NewRedactor()
	r.Register(secrets...)

	out, err := io.ReadAll(r.Stream(&chunks{parts: parts}))
	if err != nil {
		t.Fatalf("reading the filtered stream: %v", err)
	}
	return string(out)
}

func TestStreamRedactsASecretSplitAcrossTwoReads(t *testing.T) {
	// The boundary is inside the value, so neither half matches on its own.
	// A filter that scrubbed whatever each read brought in would write both
	// halves out and leak the whole thing.
	got := filtered(t, []string{"hunter2-hunter2"},
		"connecting with hunter2", "-hunter2 now\n")

	if strings.Contains(got, "hunter2-hunter2") {
		t.Errorf("the secret survived a read boundary: %q", got)
	}
	if !strings.Contains(got, "connecting with "+domain.Redacted+" now") {
		t.Errorf("the line did not survive the redaction: %q", got)
	}
}

func TestStreamDropsAnUnterminatedLinePastTheBound(t *testing.T) {
	// A line that never ends cannot be matched against anything, so the
	// filter fails closed: the bytes are dropped and a marker says so. A
	// filter that gave up and passed them through would leak precisely
	// when a service prints something enormous, which is not a coincidence
	// worth risking.
	huge := strings.Repeat("x", logging.MaxRedactedLine+1) + "hunter2-hunter2"
	got := filtered(t, []string{"hunter2-hunter2"}, huge, "\nafter\n")

	if strings.Contains(got, "hunter2-hunter2") {
		t.Errorf("the secret at the end of an oversized line was emitted: %q", got)
	}
	if strings.Contains(got, "xxxx") {
		t.Error("the oversized line was emitted rather than dropped")
	}
	if !strings.Contains(got, "dropped") {
		t.Errorf("nothing said the line was dropped: %q", got)
	}
	// And the stream carries on. A filter that stopped at the first
	// oversized line would take the rest of the logs with it.
	if !strings.Contains(got, "after") {
		t.Errorf("the stream did not recover after the dropped line: %q", got)
	}
}

func TestStreamKeepsEverythingItHasNoSecretFor(t *testing.T) {
	// The control. Without it, every assertion above is also satisfied by a
	// filter that emits nothing at all.
	const body = "demo-app-1  | started\ndemo-db-1  | ready\n"
	if got := filtered(t, []string{"hunter2-hunter2"}, body); got != body {
		t.Errorf("the filter changed a stream holding no secret:\n got %q\nwant %q", got, body)
	}
}

func TestStreamEmitsAFinalLineWithNoNewline(t *testing.T) {
	// A stream that ends mid-line still owes the caller those bytes, and
	// owes them without a terminator it invented: `morzer logs | wc -l` is
	// counting what the container wrote.
	if got := filtered(t, nil, "no newline here"); got != "no newline here" {
		t.Errorf("the last partial line was changed or lost: %q", got)
	}
}

func TestStreamRedactsTheFinalLineToo(t *testing.T) {
	got := filtered(t, []string{"hunter2-hunter2"}, "tail: hunter2-hunter2")
	if strings.Contains(got, "hunter2-hunter2") {
		t.Errorf("a secret on an unterminated final line was emitted: %q", got)
	}
}

func TestStreamCloseClosesTheSource(t *testing.T) {
	// The caller closes one thing. If the wrapper swallowed Close, the
	// runtime's `compose logs --follow` would keep running for the life of
	// the session.
	src := &closeWitness{}
	if err := logging.NewRedactor().Stream(src).Close(); err != nil {
		t.Fatal(err)
	}
	if !src.closed {
		t.Error("closing the filter did not close the stream underneath it")
	}
}

type closeWitness struct{ closed bool }

func (c *closeWitness) Read([]byte) (int, error) { return 0, io.EOF }
func (c *closeWitness) Close() error             { c.closed = true; return nil }
