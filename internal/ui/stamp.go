package ui

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// StampLines writes each line to w after the time it began, in UTC RFC 3339:
// for output nothing else times, such as a job's log on a Mac, which launchd
// writes as it comes (logs/macos-host.md). An empty line stays empty.
func StampLines(w io.Writer, now func() time.Time) *Stamper {
	return &Stamper{w: w, now: now, start: true}
}

// Stamper is the writer StampLines returns.
type Stamper struct {
	mu    sync.Mutex
	w     io.Writer
	now   func() time.Time
	start bool // the next byte written begins a line
	off   bool
}

// Off passes everything written from now on through as it is, for output
// whose format is a contract of its own, such as the `--json` envelope.
func (s *Stamper) Off() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.off = true
}

func (s *Stamper) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.off {
		return s.w.Write(p)
	}
	var b bytes.Buffer
	for rest := p; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		if s.start && i != 0 {
			b.WriteString(s.now().UTC().Format(time.RFC3339))
			b.WriteByte(' ')
		}
		if i < 0 {
			b.Write(rest)
			s.start = false
			break
		}
		b.Write(rest[:i+1])
		rest = rest[i+1:]
		s.start = true
	}
	if _, err := s.w.Write(b.Bytes()); err != nil {
		return 0, err
	}
	return len(p), nil
}
