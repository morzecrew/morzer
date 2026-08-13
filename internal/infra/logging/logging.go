// Package logging configures log/slog for the manager.
//
// Structured logging stays stdlib. What this package adds is a redaction
// handler that wraps whichever handler is chosen and scrubs registered secret
// values from every record before it is written -- the last line of defence
// behind domain.Secret's LogValue.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
)

// Format selects the output encoding.
type Format string

const (
	FormatText Format = "text"
	FormatJSON Format = "json"
)

// Options configures the logger.
type Options struct {
	Level   slog.Level
	Format  Format
	Writer  io.Writer
	AddTime bool
}

// contextKey scopes logger values in a context.
type contextKey struct{ name string }

var loggerKey = contextKey{"logger"}

// New builds the logger, wrapping the chosen handler in the redactor.
func New(opts Options) (*slog.Logger, *Redactor) {
	if opts.Writer == nil {
		opts.Writer = io.Discard
	}

	handlerOpts := &slog.HandlerOptions{Level: opts.Level}
	if !opts.AddTime {
		// Under systemd the journal timestamps every line itself, so a
		// second timestamp is noise.
		handlerOpts.ReplaceAttr = func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		}
	}

	var base slog.Handler
	switch opts.Format {
	case FormatJSON:
		base = slog.NewJSONHandler(opts.Writer, handlerOpts)
	default:
		base = slog.NewTextHandler(opts.Writer, handlerOpts)
	}

	redactor := NewRedactor()
	return slog.New(&redactingHandler{inner: base, redactor: redactor}), redactor
}

// Redactor holds the secret values to scrub. It is shared between the log
// handler and the exec runner so registering a secret once protects both.
//
// It is safe for concurrent use: secrets are registered when an operation
// loads them, while log records may already be flowing from other goroutines.
type Redactor struct {
	mu     sync.RWMutex
	values []string
}

func NewRedactor() *Redactor { return &Redactor{} }

// Register adds values to scrub. Values shorter than the minimum are ignored:
// redacting a four-character string would riddle the log with placeholders
// while protecting something anyone could guess.
func (r *Redactor) Register(values ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range values {
		if len(v) < minRedactLength {
			continue
		}
		if !contains(r.values, v) {
			r.values = append(r.values, v)
		}
	}
	// Longest first, so a short secret that is a substring of a longer one
	// cannot fragment the longer one past recognition.
	for i := 1; i < len(r.values); i++ {
		for j := i; j > 0 && len(r.values[j]) > len(r.values[j-1]); j-- {
			r.values[j], r.values[j-1] = r.values[j-1], r.values[j]
		}
	}
}

// RegisterSet registers every value in a secret set.
func (r *Redactor) RegisterSet(set domain.SecretSet) {
	r.Register(set.RedactionList()...)
}

// Values returns a snapshot, for handing to the exec runner.
func (r *Redactor) Values() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.values))
	copy(out, r.values)
	return out
}

// Apply scrubs a string.
func (r *Redactor) Apply(s string) string {
	clean, _ := r.ApplyCount(s)
	return clean
}

// ApplyCount scrubs a string and reports how many occurrences it replaced.
//
// The count exists for the support bundle, which records it per file: an
// operator deciding whether an archive is safe to send looks at that number
// first. It is deliberately a count of *replacements*, not of distinct secrets
// -- a log line holding one password twice is a line where two copies had to go.
//
// A zero is not proof that a file was clean, and the documentation says so. It
// is proof that no registered value appeared in it, which is a smaller claim and
// the only one this can honestly make.
func (r *Redactor) ApplyCount(s string) (string, int) {
	if s == "" {
		return s, 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, v := range r.values {
		if n := strings.Count(s, v); n > 0 {
			count += n
			s = strings.ReplaceAll(s, v, domain.Redacted)
		}
	}
	return s, count
}

const minRedactLength = 6

func contains(hs []string, needle string) bool {
	for _, h := range hs {
		if h == needle {
			return true
		}
	}
	return false
}

// redactingHandler wraps another handler and scrubs both the message and every
// string attribute value.
type redactingHandler struct {
	inner    slog.Handler
	redactor *Redactor
}

func (h *redactingHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.inner.Enabled(ctx, l)
}

func (h *redactingHandler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, h.redactor.Apply(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(h.redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *redactingHandler) redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve() // runs LogValuer, so domain.Secret becomes [redacted] here
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.redactor.Apply(v.String()))
	case slog.KindGroup:
		attrs := v.Group()
		out := make([]any, 0, len(attrs))
		for _, inner := range attrs {
			out = append(out, h.redactAttr(inner))
		}
		return slog.Group(a.Key, out...)
	case slog.KindAny:
		// Anything that stringifies could carry a secret through its
		// String method, so it is rendered here and scrubbed rather
		// than left for the handler to format unscrubbed.
		//
		// fmt.Stringer is in this list because leaving it out was a
		// real hole: the comment above claimed every stringifying value
		// was covered while the code handled only string and error, so
		// a struct with a String method printed its secret in full.
		// Found by the test that names each route a secret can take.
		switch t := v.Any().(type) {
		case string:
			return slog.String(a.Key, h.redactor.Apply(t))
		case error:
			return slog.String(a.Key, h.redactor.Apply(t.Error()))
		case fmt.Stringer:
			return slog.String(a.Key, h.redactor.Apply(t.String()))
		}
		// Anything else is rendered by the handler, and %v on an
		// arbitrary value can still reach a String method one level
		// down. Scrubbing the rendering is the only way to be sure.
		return slog.String(a.Key, h.redactor.Apply(fmt.Sprintf("%v", v.Any())))
	default:
		return a
	}
}

func (h *redactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		clean[i] = h.redactAttr(a)
	}
	return &redactingHandler{inner: h.inner.WithAttrs(clean), redactor: h.redactor}
}

func (h *redactingHandler) WithGroup(name string) slog.Handler {
	return &redactingHandler{inner: h.inner.WithGroup(name), redactor: h.redactor}
}

// WithLogger returns a context carrying the logger.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// FromContext returns the context's logger, or a discarding one. It never
// returns nil, so call sites do not need a nil check around every log line.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.New(discardHandler{})
}

// WithOperation returns a context whose logger tags every record with the
// operation, so a journal line and a log line can be correlated.
func WithOperation(ctx context.Context, opID string, opType domain.OperationType) context.Context {
	l := FromContext(ctx).With("operation_id", opID, "operation_type", string(opType))
	return WithLogger(ctx, l)
}

// WithStep adds the step to the context logger.
func WithStep(ctx context.Context, stepID string) context.Context {
	return WithLogger(ctx, FromContext(ctx).With("step_id", stepID))
}

// discardHandler drops everything. Cheaper than slog.NewTextHandler(io.Discard)
// because Enabled short-circuits before a record is built.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler           { return d }

// EventSink logs every engine event, so the log holds the complete story of an
// operation regardless of which presenter the operator was watching.
func EventSink(l *slog.Logger) events.Sink {
	return events.SinkFunc(func(e events.Event) {
		level := slog.LevelInfo
		switch e.Level {
		case events.LevelDebug:
			level = slog.LevelDebug
		case events.LevelWarn:
			level = slog.LevelWarn
		case events.LevelError:
			level = slog.LevelError
		}

		attrs := []any{"kind", string(e.Kind)}
		if e.OpID != "" {
			attrs = append(attrs, "operation_id", e.OpID)
		}
		if e.StepID != "" {
			attrs = append(attrs, "step_id", e.StepID)
		}
		if e.Status != "" {
			attrs = append(attrs, "status", e.Status)
		}
		if e.Duration > 0 {
			attrs = append(attrs, "duration_ms", e.Duration.Milliseconds())
		}
		if e.Err != nil {
			attrs = append(attrs, "error", e.Err.Error(), "code", string(e.Err.Code))
		}

		msg := e.Message
		if msg == "" {
			msg = e.Description
		}
		if msg == "" {
			msg = string(e.Kind)
		}

		// The level-specific methods rather than Log(ctx, ...): a bus sink is
		// invoked from wherever an event was published and has no operation
		// context to carry, so passing a background one would be a fiction.
		switch level {
		case slog.LevelDebug:
			l.Debug(msg, attrs...)
		case slog.LevelWarn:
			l.Warn(msg, attrs...)
		case slog.LevelError:
			l.Error(msg, attrs...)
		default:
			l.Info(msg, attrs...)
		}
	})
}

// ParseLevel maps a verbosity flag to a level.
func ParseLevel(verbose, quiet bool) slog.Level {
	switch {
	case quiet:
		return slog.LevelError
	case verbose:
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

// Clock is injected wherever timestamps affect behaviour, so tests can make
// time deterministic instead of sleeping.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// SystemClock is the production clock.
var SystemClock Clock = realClock{}
