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
	CodeInternal          Code = "internal"
	CodeUsage             Code = "usage"
	CodePreflight         Code = "preflight"
	CodeLocked            Code = "locked"
	CodeInstallation      Code = "installation"
	CodeSecrets           Code = "secrets"
	CodeRuntime           Code = "runtime"
	CodeHealth            Code = "health"
	CodeIncompatible      Code = "incompatible"
	CodeBackup            Code = "backup"
	CodeCompensated       Code = "compensated"
	CodeManualIntervetion Code = "manual-intervention"
	CodeInterrupted       Code = "interrupted"
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
	return newf(CodeManualIntervetion, CategorySystem, ErrManualIntervention, cause, format, args...)
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
