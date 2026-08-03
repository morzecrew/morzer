// Package jsonout implements the machine-readable output contract.
//
// The contract is narrow on purpose: stdout carries exactly one JSON object,
// written once, at the end. Streaming partial results would mean a consumer
// has to decide when the output is complete; a single object means `morzer
// doctor --json | jq` either gets a document or gets nothing.
//
// Events may additionally be streamed to stderr as JSONL, which is where a
// supervisor watching a long operation looks.
package jsonout

import (
	"encoding/json"
	"io"
	"sync"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
)

// Envelope is the single object written to stdout.
//
// Every command produces this shape. A consumer can therefore check `ok` and
// `error` identically regardless of which command ran, which is the property
// that makes it scriptable.
type Envelope struct {
	// OK is the headline: did the command do what was asked.
	OK bool `json:"ok"`

	// Command is the invoked command path, e.g. "apply" or "secret set".
	Command string `json:"command"`

	// ManagerVersion and APIVersions let a consumer detect a manager it
	// does not understand without a second invocation.
	ManagerVersion string   `json:"manager_version"`
	APIVersions    []string `json:"supported_api_versions,omitempty"`

	// Data is the command-specific payload.
	Data any `json:"data,omitempty"`

	// Operation is the journal record for mutating commands.
	Operation *domain.OperationRecord `json:"operation,omitempty"`

	// Error is the full structured error when the command failed.
	Error *domain.Error `json:"error,omitempty"`

	// ExitCode is the process status, included so a consumer parsing
	// output does not also have to capture the exit status separately.
	ExitCode int `json:"exit_code"`

	// Events is the operation's event stream, included only when
	// explicitly requested: it is verbose, and most consumers want the
	// result rather than the narration.
	Events []events.Event `json:"events,omitempty"`
}

// Presenter buffers events and writes the envelope on close.
type Presenter struct {
	mu sync.Mutex

	out io.Writer

	// stream, when set, receives each event as JSONL on stderr while the
	// operation runs.
	stream io.Writer

	collected   []events.Event
	includeAll  bool
	managerVer  string
	apiVersions []string
}

// Options configures the JSON presenter.
type Options struct {
	// Out receives the single result object. This must be stdout.
	Out io.Writer

	// EventStream receives JSONL events as they happen. This must be
	// stderr, or nil to disable streaming.
	EventStream io.Writer

	// IncludeEvents embeds the full event list in the envelope.
	IncludeEvents bool

	ManagerVersion string
	APIVersions    []string
}

func New(opts Options) *Presenter {
	return &Presenter{
		out:         opts.Out,
		stream:      opts.EventStream,
		includeAll:  opts.IncludeEvents,
		managerVer:  opts.ManagerVersion,
		apiVersions: opts.APIVersions,
	}
}

var _ events.Sink = (*Presenter)(nil)

// Handle records an event and optionally streams it.
func (p *Presenter) Handle(e events.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.includeAll {
		p.collected = append(p.collected, e)
	}

	if p.stream == nil {
		return
	}
	// Encoding failures are dropped rather than propagated: a presenter is
	// a subscriber, and one malformed event must not fail an operation.
	if data, err := json.Marshal(e); err == nil {
		_, _ = p.stream.Write(append(data, '\n'))
	}
}

// Write emits the envelope. It is called exactly once, by the CLI layer, after
// the command has finished.
func (p *Presenter) Write(command string, data any, record *domain.OperationRecord, err error) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	env := Envelope{
		OK:             err == nil,
		Command:        command,
		ManagerVersion: p.managerVer,
		APIVersions:    p.apiVersions,
		Data:           data,
		Operation:      record,
		ExitCode:       domain.ExitCode(err),
	}
	if err != nil {
		env.Error = domain.AsError(err)
	}
	if p.includeAll {
		env.Events = p.collected
	}

	enc := json.NewEncoder(p.out)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}
