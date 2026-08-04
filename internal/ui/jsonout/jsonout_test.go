package jsonout_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/events"
	"github.com/morzecrew/morzer/internal/ui/jsonout"
)

// `--json` is a machine contract: stdout carries exactly one object, narration
// goes to stderr, and the exit code in the envelope matches the process's. A
// consumer that has to tolerate a stray line on stdout is a consumer nobody
// writes.

func TestTheEnvelopeIsExactlyOneObjectOnStdout(t *testing.T) {
	var out, stream bytes.Buffer
	p := jsonout.New(jsonout.Options{
		Out: &out, EventStream: &stream, IncludeEvents: true,
		ManagerVersion: "1.2.0", APIVersions: []string{"selfhost/v1alpha1"},
	})

	// Events arrive while the operation runs and must not reach stdout.
	p.Handle(events.OperationStarted("op_01", domain.OpTypeApply, "apply", []string{"one"}, false))
	p.Handle(events.Message(events.LevelWarn, "a warning"))

	if err := p.Write("morzer apply", map[string]string{"k": "v"}, nil, nil); err != nil {
		t.Fatalf("Write: %v", err)
	}

	dec := json.NewDecoder(strings.NewReader(out.String()))
	var env map[string]any
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("stdout is not a JSON object: %v\n%s", err, out.String())
	}
	if dec.More() {
		t.Fatalf("stdout carries more than one value:\n%s", out.String())
	}

	if env["ok"] != true || env["command"] != "morzer apply" {
		t.Errorf("envelope = %v", env)
	}
	if env["manager_version"] != "1.2.0" {
		t.Errorf("the manager version is missing: %v", env["manager_version"])
	}

	// The event stream is a separate channel, and it carried the narration
	// as JSONL for anything watching live.
	if stream.Len() == 0 {
		t.Error("events were requested but nothing was streamed")
	}

	// Under --verbose the events are also *inside* the envelope. That is
	// the point of the flag; what must never happen is a second value on
	// stdout, which the single-object check above is what guards.
	collected, ok := env["events"].([]any)
	if !ok || len(collected) != 2 {
		t.Errorf("the envelope carries %v events, want the two that were handled", env["events"])
	}
}

func TestAFailureIsStillExactlyOneObject(t *testing.T) {
	var out bytes.Buffer
	p := jsonout.New(jsonout.Options{Out: &out, ManagerVersion: "1.2.0"})

	failure := domain.HealthError(nil, "api did not become healthy").
		WithHint("check `docker compose logs api`")

	if err := p.Write("morzer apply", nil, nil, failure); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("a failing run did not produce an object: %v\n%s", err, out.String())
	}
	if env["ok"] != false {
		t.Error("a failure was reported ok")
	}

	// The exit code is in the envelope so a consumer reading only stdout
	// does not have to also capture the process status.
	code, ok := env["exit_code"].(float64)
	if !ok || int(code) != domain.ExitCode(failure) {
		t.Errorf("exit_code = %v, want %d", env["exit_code"], domain.ExitCode(failure))
	}

	errObj, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("the error is not an object: %v", env["error"])
	}
	// The hint is the actionable half, and is what a consumer surfaces to a
	// human. Dropping it makes the envelope strictly less useful than the
	// text output.
	if errObj["hint"] == nil || errObj["message"] == nil {
		t.Errorf("the error carries no message or hint: %v", errObj)
	}
}

func TestEventsAreOmittedUnlessAskedFor(t *testing.T) {
	var out bytes.Buffer
	p := jsonout.New(jsonout.Options{Out: &out})

	p.Handle(events.Message(events.LevelInfo, "narration"))
	if err := p.Write("morzer status", nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(out.String(), "narration") {
		t.Errorf("events appeared without --verbose:\n%s", out.String())
	}
}

func TestTheOperationRecordTravelsInTheEnvelope(t *testing.T) {
	var out bytes.Buffer
	p := jsonout.New(jsonout.Options{Out: &out})

	rec := domain.OperationRecord{ID: "op_01", Type: domain.OpTypeUpdate, Status: domain.StatusSucceeded}
	if err := p.Write("morzer update", nil, &rec, nil); err != nil {
		t.Fatal(err)
	}

	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	op, ok := env["operation"].(map[string]any)
	if !ok {
		t.Fatalf("the operation record is missing: %v", env)
	}
	// The id is what an operator passes to --resume and looks up in the
	// journal, so it has to survive.
	if op["id"] != "op_01" {
		t.Errorf("operation.id = %v", op["id"])
	}
}
