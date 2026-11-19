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
	// owed is the rest of a time a failed write began, written ahead of
	// the next byte rather than a second time.
	owed []byte
}

// Off passes everything written from now on through as it is, for output
// whose format is a contract of its own, such as the `--json` envelope.
func (s *Stamper) Off() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.off = true
}

// Write writes p in one write to w, a time ahead of each line, so that a
// record written beside it can never land inside one. When w fails, it
// reports the bytes of p that reached it, and stands where they left it.
func (s *Stamper) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.off {
		return s.w.Write(p)
	}
	if len(p) == 0 {
		return 0, nil
	}

	type part struct{ prefix, text []byte }
	var parts []part
	var b bytes.Buffer
	start, owed := s.start, s.owed
	for rest := p; len(rest) > 0; {
		i := bytes.IndexByte(rest, '\n')
		text := rest
		if i >= 0 {
			text = rest[:i+1]
		}
		var prefix []byte
		switch {
		case owed != nil:
			prefix, owed = owed, nil
		case start && i != 0:
			prefix = []byte(s.now().UTC().Format(time.RFC3339) + " ")
		}
		parts = append(parts, part{prefix, text})
		b.Write(prefix)
		b.Write(text)
		start = text[len(text)-1] == '\n'
		rest = rest[len(text):]
	}

	m, err := s.w.Write(b.Bytes())
	if err == nil && m < b.Len() {
		err = io.ErrShortWrite
	}
	if err == nil {
		s.start, s.owed = start, owed
		return len(p), nil
	}
	// Walked as far as w took it: the parts it took whole, then the one it
	// stopped in.
	n, at := 0, 0
	s.owed = nil
	for _, pt := range parts {
		if m < at+len(pt.prefix) {
			s.owed = pt.prefix[m-at:]
			return n, err
		}
		at += len(pt.prefix)
		if m < at+len(pt.text) {
			// Mid-line once a time or a byte of it is out; otherwise
			// where the part before left it.
			if m > at || len(pt.prefix) > 0 {
				s.start = false
			}
			return n + m - at, err
		}
		n += len(pt.text)
		at += len(pt.text)
		s.start = pt.text[len(pt.text)-1] == '\n'
	}
	return n, err
}
