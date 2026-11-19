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
func StampLines(w io.Writer, now func() time.Time) io.Writer {
	return &stamper{w: w, now: now, start: true}
}

type stamper struct {
	mu    sync.Mutex
	w     io.Writer
	now   func() time.Time
	start bool // the next byte written begins a line
}

func (s *stamper) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
