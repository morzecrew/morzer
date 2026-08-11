package logging

import (
	"bytes"
	"fmt"
	"io"
)

// MaxRedactedLine bounds how much of an unterminated line the stream filter
// will hold.
//
// A line longer than this is dropped rather than passed through, so the bound
// is also the size of the largest single log line the manager will relay.
// 64 KiB is comfortably above any line a service writes deliberately and well
// below what a runaway one produces.
const MaxRedactedLine = 64 << 10

// oversizeMarker says a line was dropped, and how long it was allowed to be.
//
// Built from the bound rather than repeating it, because an operator who sees
// this is about to ask whether the manager or the service ate their output --
// and a marker naming a number the code no longer uses would answer wrongly.
func oversizeMarker() string {
	return fmt.Sprintf("[morzer: a log line longer than %d bytes was dropped rather "+
		"than passed through unredacted]", MaxRedactedLine)
}

// Stream scrubs registered secret values from a byte stream, line by line.
//
// A stream filter and not a call to Apply per read, because a read boundary
// falls wherever the kernel put it: a secret split across two reads matches
// neither half, and a filter that worked on whatever arrived would leak
// precisely the values it exists to catch. Bytes are therefore held until a
// newline and matched whole.
//
// Bounded, and fail-closed at the bound. An unterminated line past
// MaxRedactedLine is dropped with a marker rather than emitted unmatched: a
// filter that gave up and passed the bytes through would be one that leaks
// exactly when a service prints something enormous, which is not a coincidence
// worth risking.
//
// Closing the returned reader closes the source, so the caller keeps one thing
// to close and the runtime's process is still reaped.
func (r *Redactor) Stream(src io.ReadCloser) io.ReadCloser {
	return &redactingStream{src: src, redactor: r}
}

type redactingStream struct {
	src      io.ReadCloser
	redactor *Redactor

	// line is the partial line held back from the caller; out is what is
	// ready for it.
	line bytes.Buffer
	out  bytes.Buffer

	// dropping is set while the rest of an oversized line is discarded. The
	// marker is written when the bound trips rather than when the line
	// finally ends, so an operator following a stream sees it immediately.
	dropping bool

	// err is the source's terminal error, delivered only once everything
	// held back has been handed over.
	err error

	chunk [32 << 10]byte
}

func (s *redactingStream) Read(p []byte) (int, error) {
	for s.out.Len() == 0 && s.err == nil {
		n, err := s.src.Read(s.chunk[:])
		if n > 0 {
			s.consume(s.chunk[:n])
		}
		if err != nil {
			s.err = err
			s.flush()
		}
	}
	if s.out.Len() > 0 {
		return s.out.Read(p)
	}
	return 0, s.err
}

// consume splits what arrived into lines and redacts each whole one.
func (s *redactingStream) consume(data []byte) {
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			s.hold(data)
			return
		}
		s.hold(data[:i])
		s.emitLine(true)
		data = data[i+1:]
	}
}

// hold appends to the partial line, tripping the bound rather than growing
// without limit.
func (s *redactingStream) hold(data []byte) {
	if s.dropping {
		return
	}
	if s.line.Len()+len(data) > MaxRedactedLine {
		// The whole line goes, including the part already held: half a
		// line is where a secret straddling the bound would survive.
		s.line.Reset()
		s.dropping = true
		s.out.WriteString(oversizeMarker())
		s.out.WriteByte('\n')
		return
	}
	s.line.Write(data)
}

// emitLine hands one complete line to the caller, redacted.
func (s *redactingStream) emitLine(terminated bool) {
	if s.dropping {
		// The tail of a line whose marker has already been written.
		s.dropping = false
		s.line.Reset()
		return
	}
	if s.line.Len() == 0 && !terminated {
		return
	}
	s.out.WriteString(s.redactor.Apply(s.line.String()))
	if terminated {
		s.out.WriteByte('\n')
	}
	s.line.Reset()
}

// flush releases whatever was held when the source ended.
//
// A final line with no newline is emitted without one: the stream is bytes, and
// inventing a terminator would change what a consumer counting lines sees.
func (s *redactingStream) flush() { s.emitLine(false) }

func (s *redactingStream) Close() error { return s.src.Close() }
