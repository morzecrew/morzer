// Package domain holds the manager's types and rules: the release manifest,
// the installation, operations, versions and the error model.
//
// It is pure. Nothing here performs I/O, and it imports nothing from this
// repository and nothing beyond the standard library and a semver parser. That
// restriction is what lets the rules every other layer depends on -- manifest
// validation, compatibility, exit-code mapping -- be tested in isolation, and
// it is enforced by depguard rather than by discipline.
package domain

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable error code. It is part of the public
// contract: operator scripts and monitoring may match on it, so codes are
// never renamed or repurposed once published.
type Code string

const (
	CodeInternal           Code = "internal"
	CodeUsage              Code = "usage"
	CodePreflight          Code = "preflight"
	CodeLocked             Code = "locked"
	CodeInstallation       Code = "installation"
	CodeSecrets            Code = "secrets"
	CodeRuntime            Code = "runtime"
	CodeHealth             Code = "health"
	CodeIncompatible       Code = "incompatible"
	CodeBackup             Code = "backup"
	CodeCompensated        Code = "compensated"
	CodeManualIntervention Code = "manual-intervention"
	CodeInterrupted        Code = "interrupted"
)

// Category groups codes for reporting. It carries no control-flow meaning.
type Category string

const (
	CategoryUser     Category = "user"     // the operator can fix this directly
	CategorySystem   Category = "system"   // the machine or an external tool is at fault
	CategoryBug      Category = "bug"      // the manager itself is at fault
	CategoryConflict Category = "conflict" // another actor holds the resource
)

// Sentinel errors. Every typed error wraps exactly one of these, so exit-code
// mapping in main is a flat errors.Is chain rather than a type switch.
var (
	ErrInternal            = errors.New("internal error")
	ErrUsage               = errors.New("usage error")
	ErrPreflight           = errors.New("preflight check failed")
	ErrLocked              = errors.New("deployment lock held by another operation")
	ErrInstallation        = errors.New("installation missing or corrupted")
	ErrAlreadyInstalled    = errors.New("installation already exists")
	ErrSecrets             = errors.New("secrets error")
	ErrRuntime             = errors.New("container runtime error")
	ErrHealth              = errors.New("health check failed")
	ErrIncompatible        = errors.New("incompatible release")
	ErrBackup              = errors.New("backup or restore failed")
	ErrCompensated         = errors.New("operation failed, compensation succeeded")
	ErrManualIntervention  = errors.New("manual intervention required")
	ErrInterrupted         = errors.New("interrupted")
	ErrUnsupported         = errors.New("unsupported operation")
	ErrValidation          = errors.New("validation failed")
	ErrNotFound            = errors.New("not found")
	ErrSecretNotFound      = errors.New("secret not found")
	ErrReleaseNotFound     = errors.New("release not found")
	ErrDigestMismatch      = errors.New("content digest mismatch")
	ErrUnknownAPIVersion   = errors.New("unknown manifest api_version")
	ErrPathEscape          = errors.New("path escapes its root")
	ErrIrreversible        = errors.New("operation is not reversible")
	ErrToolMissing         = errors.New("required tool missing")
	ErrToolIncompatible    = errors.New("required tool version incompatible")
	ErrOperationIncomplete = errors.New("a previous operation did not finish")

	// ErrNoSigningKey marks a machine that has no signing identity yet.
	//
	// **Not a corruption and not a failure.** Every installation that
	// reached schema 6 by migration is in this state, because the migration
	// mints nothing (RFC 0028 decision 9) -- a machine acquires a key the
	// first time it is asked to sign, not on the upgrade that made keys
	// possible. A sentinel rather than an empty return value so a caller has
	// to decide which of the two it means: `status` reports it, and a signer
	// mints.
	//
	// Distinct from a key that disagrees with recorded state, which is
	// ErrSigningKeyMismatch and is a machine to stop.
	ErrNoSigningKey = errors.New("installation has no signing key")

	// ErrSigningKeyMismatch marks a signing key file whose public half is
	// not the one installation state records.
	//
	// This is the refusal RFC 0028 §5.4 asks for, and it is narrower than
	// "there is no key": such a machine would sign with one key while
	// telling everybody -- through `status`, the export, an attestation --
	// that it signs with another, and its artifacts are attributable to
	// nobody. Absence is ordinary; disagreement is not.
	ErrSigningKeyMismatch = errors.New("signing key does not match recorded public key")

	// ErrTemplateSyntax marks a manifest template that does not parse, as
	// opposed to one that parses and refers to something absent.
	//
	// Manifest validation checks only the first: it runs without an
	// installation, so no parameter has a value yet and "unresolvable" says
	// nothing. Telling the two apart used to be a substring match on the
	// message, which made rewording the message silently disable the check.
	ErrTemplateSyntax = errors.New("template does not parse")

	// ErrTemplateRender marks a template that parses and then fails against
	// a context: a missing key, a field that does not exist on the type, a
	// `required` that was not satisfied.
	//
	// Distinct from ErrTemplateSyntax because the two carry different
	// promises. A parse failure is unconditional -- that template cannot
	// render anywhere. A render failure under `verify --render-check` is
	// against a *synthetic* context whose values are invented, so it is a
	// smoke test result rather than a verdict about the operator's machine
	// (RFC 0013 decision 12), and a caller that could not tell them apart
	// would have to over-claim about one of them.
	ErrTemplateRender = errors.New("template does not render")

	// ErrMeasureIncomplete marks a measurement that did not run, as opposed
	// to one that ran and could not produce an answer.
	//
	// The distinction exists for the backup space check, and only the
	// permissive side is marked. A measurement the manager could not even
	// attempt says nothing about the volume and may recur or not; a
	// measurement that ran and returned something unusable is a property of
	// this helper in this environment, and will say the same thing
	// tomorrow. The check refuses on anything it does not recognise, so a
	// failure mode nobody has thought of yet -- and every other
	// implementation of the port -- lands on the safe side by default.
	ErrMeasureIncomplete = errors.New("the measurement did not run")
)

// Error is the manager's structured error. Message says what happened; Hint
// says what to do about it. Both are operator-facing, so neither may contain
// secret values -- callers redact before constructing.
type Error struct {
	Code     Code     `json:"code"`
	Category Category `json:"category"`
	Message  string   `json:"message"`
	Hint     string   `json:"hint,omitempty"`
	OpID     string   `json:"operation_id,omitempty"`
	StepID   string   `json:"step_id,omitempty"`

	// Err is the wrapped cause. It always chains to exactly one sentinel.
	Err error `json:"-"`
}

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// WithHint returns a copy carrying an operator-facing remedy.
func (e *Error) WithHint(format string, args ...any) *Error {
	c := *e
	c.Hint = fmt.Sprintf(format, args...)
	return &c
}

// WithHintFrom returns a copy carrying the cause's remedy, when the cause has
// one and this error does not.
//
// Wrapping is how an error gains context on the way out, and AsError reports
// the outermost structured error -- so a wrap with no hint of its own silently
// discards the one sentence that told the operator what to do. That is not
// hypothetical: "the volume helper image is not on this machine" carried
// `docker pull <ref>`, and wrapping it as "cannot capture volume uploads" left
// an air-gapped operator with a diagnosis and no remedy.
func (e *Error) WithHintFrom(cause error) *Error {
	if e.Hint != "" || cause == nil {
		return e
	}
	hint := firstHint(cause)
	if hint == "" {
		return e
	}
	c := *e
	c.Hint = hint
	return &c
}

// firstHint walks a cause chain for the nearest remedy.
//
// The whole chain, not the outermost *Error: errors.As -- what AsError uses --
// stops at the first structured error it meets, so a hint behind an
// intermediate wrap that has none is discarded. Two wraps is the normal depth
// once an adapter's error passes through a capture step on its way to the
// operator, which is precisely the case WithHintFrom exists to survive.
//
// The []error branch is not optional: every constructed Error wraps
// `fmt.Errorf("%w: %w", sentinel, cause)`, so the cause hangs off a
// multi-unwrap node and a single-Unwrap walk would never reach it.
func firstHint(err error) string {
	for err != nil {
		if e, ok := err.(*Error); ok && e.Hint != "" {
			return e.Hint
		}
		switch u := err.(type) {
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		case interface{ Unwrap() []error }:
			for _, w := range u.Unwrap() {
				if hint := firstHint(w); hint != "" {
					return hint
				}
			}
			return ""
		default:
			return ""
		}
	}
	return ""
}

// WithOp returns a copy tagged with the operation and step it arose in. The
// engine calls this as errors travel outward, so the operator gets a location
// without every call site having to thread the operation through.
func (e *Error) WithOp(opID, stepID string) *Error {
	c := *e
	c.OpID, c.StepID = opID, stepID
	return &c
}

// newf builds an Error whose cause chain includes both sentinel and cause.
func newf(code Code, cat Category, sentinel error, cause error, format string, args ...any) *Error {
	chained := sentinel
	if cause != nil {
		// Wrapping both keeps errors.Is working against the sentinel for
		// exit-code mapping and against the cause for diagnosis.
		chained = fmt.Errorf("%w: %w", sentinel, cause)
	}
	return &Error{
		Code:     code,
		Category: cat,
		Message:  fmt.Sprintf(format, args...),
		Err:      chained,
	}
}

// Constructors. One per exit-code class, so producing an error that maps to a
// bogus exit code takes deliberate effort.

func Internal(cause error, format string, args ...any) *Error {
	return newf(CodeInternal, CategoryBug, ErrInternal, cause, format, args...)
}

func Usage(format string, args ...any) *Error {
	return newf(CodeUsage, CategoryUser, ErrUsage, nil, format, args...)
}

func ValidationError(cause error, format string, args ...any) *Error {
	return newf(CodeUsage, CategoryUser, ErrValidation, cause, format, args...)
}

func Preflight(cause error, format string, args ...any) *Error {
	return newf(CodePreflight, CategoryUser, ErrPreflight, cause, format, args...)
}

func Locked(format string, args ...any) *Error {
	return newf(CodeLocked, CategoryConflict, ErrLocked, nil, format, args...)
}

func InstallationError(cause error, format string, args ...any) *Error {
	return newf(CodeInstallation, CategoryUser, ErrInstallation, cause, format, args...)
}

func SecretsError(cause error, format string, args ...any) *Error {
	return newf(CodeSecrets, CategoryUser, ErrSecrets, cause, format, args...)
}

// NoSigningKey reports a machine that has never minted a signing identity.
//
// CategoryUser: the remedy is an operator action -- run something that signs,
// or `init` -- rather than a broken machine or a bug.
func NoSigningKey(cause error, format string, args ...any) *Error {
	return newf(CodeSecrets, CategoryUser, ErrNoSigningKey, cause, format, args...)
}

// SigningKeyMismatch reports a key file that disagrees with recorded state.
//
// CategorySystem rather than User: nothing the operator typed produced this,
// and the machine is in a state where its own artifacts cannot be attributed.
func SigningKeyMismatch(cause error, format string, args ...any) *Error {
	return newf(CodeSecrets, CategorySystem, ErrSigningKeyMismatch, cause, format, args...)
}

func RuntimeError(cause error, format string, args ...any) *Error {
	return newf(CodeRuntime, CategorySystem, ErrRuntime, cause, format, args...)
}

func HealthError(cause error, format string, args ...any) *Error {
	return newf(CodeHealth, CategorySystem, ErrHealth, cause, format, args...)
}

func IncompatibleError(cause error, format string, args ...any) *Error {
	return newf(CodeIncompatible, CategoryUser, ErrIncompatible, cause, format, args...)
}

func BackupError(cause error, format string, args ...any) *Error {
	return newf(CodeBackup, CategorySystem, ErrBackup, cause, format, args...)
}

func Compensated(cause error, format string, args ...any) *Error {
	return newf(CodeCompensated, CategorySystem, ErrCompensated, cause, format, args...)
}

func ManualIntervention(cause error, format string, args ...any) *Error {
	return newf(CodeManualIntervention, CategorySystem, ErrManualIntervention, cause, format, args...)
}

func Interrupted(format string, args ...any) *Error {
	return newf(CodeInterrupted, CategoryUser, ErrInterrupted, nil, format, args...)
}

// ExitCode is the process exit status for an error. The mapping is the public
// contract from the spec's exit-code table; systemd units and CI depend on it.
//
// Order matters: the most specific sentinel wins, because a compensated
// failure also wraps the underlying runtime or health error.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return ExitSuccess
	case errors.Is(err, ErrInterrupted):
		return ExitInterrupted
	case errors.Is(err, ErrManualIntervention):
		return ExitManualIntervention
	case errors.Is(err, ErrCompensated):
		return ExitCompensated
	case errors.Is(err, ErrBackup):
		return ExitBackup
	case errors.Is(err, ErrIncompatible):
		return ExitIncompatible
	case errors.Is(err, ErrHealth):
		return ExitHealth
	case errors.Is(err, ErrRuntime):
		return ExitRuntime
	case errors.Is(err, ErrSecrets):
		return ExitSecrets
	case errors.Is(err, ErrInstallation), errors.Is(err, ErrAlreadyInstalled):
		return ExitInstallation
	case errors.Is(err, ErrLocked):
		return ExitLocked
	case errors.Is(err, ErrPreflight):
		return ExitPreflight
	case errors.Is(err, ErrUsage), errors.Is(err, ErrValidation):
		return ExitUsage
	default:
		return ExitInternal
	}
}

// Exit codes. Stable: systemd units, CI pipelines, and operator scripts
// depend on these values.
const (
	ExitSuccess            = 0
	ExitInternal           = 1
	ExitUsage              = 2
	ExitPreflight          = 3
	ExitLocked             = 4
	ExitInstallation       = 5
	ExitSecrets            = 6
	ExitRuntime            = 7
	ExitHealth             = 8
	ExitIncompatible       = 9
	ExitBackup             = 10
	ExitCompensated        = 11
	ExitManualIntervention = 12
	ExitInterrupted        = 130
)

// AsError extracts the structured error from a chain, or synthesises one so
// presenters always have a Code and Hint to render.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{
		Code:     CodeInternal,
		Category: CategoryBug,
		Message:  err.Error(),
		Err:      fmt.Errorf("%w: %w", ErrInternal, err),
	}
}
