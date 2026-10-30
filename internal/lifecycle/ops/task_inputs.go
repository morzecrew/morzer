package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/lifecycle/engine"
	"github.com/morzecrew/morzer/internal/ports"
)

// inputSpaceMargin is free space staging leaves on the filesystem beyond the
// inputs themselves, so a task's inputs never fill the disk the journal and
// the release store live on.
const inputSpaceMargin = 64 * domain.MiB

// keyStagedInputs is what stage-inputs copied, for run-task and the statement.
const keyStagedInputs = "staged-inputs"

// openInput is one `--input` as checked before the lock: the file is opened
// here, and the type check and the copy use that one descriptor.
type openInput struct {
	Name string
	File *os.File
	Size int64

	// Max is the input's max_size, or zero for none.
	Max int64
}

// stagedInputs is what the copy produced.
type stagedInputs struct {
	Dir    string
	Inputs []domain.AttestedInput
}

// openTaskInputs checks what the operator passed against what the task
// declares (RFC 0041 §5.2), and opens each file. Every refusal is exit 2 and
// comes before the lock. The caller closes the files.
func openTaskInputs(name string, task domain.TaskSpec, given map[string]string) (_ []openInput, err error) {
	for input := range given {
		if _, ok := task.Inputs[input]; !ok {
			declared := task.InputNames()
			if len(declared) == 0 {
				return nil, domain.Usage("task %s takes no inputs, and was given %s", name, input)
			}
			return nil, domain.Usage("task %s declares no input %q", name, input).
				WithHint("it declares %s; `morzer task list` describes them", strings.Join(declared, ", "))
		}
	}

	var out []openInput
	defer func() {
		if err != nil {
			closeInputs(out)
		}
	}()
	for _, input := range task.InputNames() {
		path, ok := given[input]
		if !ok {
			return out, domain.Usage("task %s needs --input %s=<path>", name, input).
				WithHint("%s", inputHint(task.Inputs[input]))
		}
		f, size, err := openRegular(path)
		if err != nil {
			return out, err
		}
		max := task.Inputs[input].MaxSize.Bytes()
		out = append(out, openInput{Name: input, File: f, Size: size, Max: max})
		if max > 0 && size > max {
			return out, domain.Usage("%s is %s, and task %s takes at most %s for %s",
				path, domain.ByteSize(size), name, domain.ByteSize(max), input)
		}
	}
	return out, nil
}

func inputHint(in domain.TaskInput) string {
	if in.Description != "" {
		return "it is: " + in.Description
	}
	return "every input the task declares is required"
}

// openRegular opens a path once and refuses anything but a regular file. Not
// blocking, so a FIFO is refused rather than waited on; a symlink is followed,
// because the operator is root already and reaches nothing new through one.
func openRegular(path string) (*os.File, int64, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, domain.Usage("cannot open %s: %v", path, unwrapPathError(err))
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, domain.Usage("cannot read %s: %v", path, unwrapPathError(err))
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, 0, domain.Usage("%s is %s, not a file", path, describeMode(info.Mode())).
			WithHint("a task takes a regular file; archive a directory and pass the archive")
	}
	return f, info.Size(), nil
}

func unwrapPathError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

func describeMode(m os.FileMode) string {
	switch {
	case m.IsDir():
		return "a directory"
	case m&os.ModeNamedPipe != 0:
		return "a named pipe"
	case m&os.ModeDevice != 0:
		return "a device"
	case m&os.ModeSocket != 0:
		return "a socket"
	default:
		return "not a regular file"
	}
}

func closeInputs(inputs []openInput) {
	for _, in := range inputs {
		_ = in.File.Close()
	}
}

// inputBudget is how much staging may write: the free space under the
// manager's directory less the margin, refused when the inputs as checked do
// not fit (§5.3 step 1). The copy is bounded by it as well as by each
// max_size, since a file can grow after it was checked.
func (d *Deps) inputBudget(inputs []openInput) (int64, error) {
	var need int64
	for _, in := range inputs {
		need += in.Size
	}
	free, err := d.freeSpace(d.Paths.ManagerDir())
	if err != nil {
		return 0, domain.Preflight(err, "cannot tell how much space is free under %s", d.Paths.ManagerDir())
	}
	budget := free - inputSpaceMargin
	if need > budget {
		return 0, domain.Preflight(nil, "the task's inputs need %s, and %s is free under %s with %s kept back",
			domain.ByteSize(need), domain.ByteSize(free), d.Paths.ManagerDir(), domain.ByteSize(inputSpaceMargin)).
			WithHint("free some space there; the inputs are copied before the task runs and removed after")
	}
	return budget, nil
}

// stepStageInputs copies each input into this operation's directory, hashing
// what it copies (§5.3). First of the task's steps, under the lock: where no
// other operation can be running, so every other directory there is one a
// killed task left behind, and it is swept first.
func stepStageInputs(d *Deps, inputs []openInput, budget int64) engine.Step {
	return engine.Step{
		ID:          "stage-inputs",
		Description: "copy the task's inputs",
		Idempotent:  true,
		OnFailure:   engine.Compensate,
		Timeout:     time.Hour,
		PlanDetail: func(ctx context.Context, st *engine.State) (string, string) {
			parts := make([]string, 0, len(inputs))
			for _, in := range inputs {
				parts = append(parts, fmt.Sprintf("%s (%s) at %s",
					in.Name, domain.ByteSize(in.Size), domain.TaskInputPath(in.Name)))
			}
			return "copy " + strings.Join(parts, ", "), ""
		},
		Execute: func(ctx context.Context, st *engine.State) error {
			if err := d.sweepInputs(st.OpID); err != nil {
				return err
			}
			dir := filepath.Join(d.Paths.InputsDir(), st.OpID)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return domain.Internal(err, "cannot create %s", dir)
			}
			// MkdirAll leaves an existing parent's mode alone, and the
			// umask trims a new one: set both.
			for _, p := range []string{d.Paths.InputsDir(), dir} {
				if err := os.Chmod(p, 0o700); err != nil {
					return domain.Internal(err, "cannot set the mode of %s", p)
				}
			}

			staged := stagedInputs{Dir: dir}
			left := budget
			for _, in := range inputs {
				bound := left
				if in.Max > 0 && in.Max < bound {
					bound = in.Max
				}
				size, sum, err := copyInput(ctx, in, filepath.Join(dir, in.Name), bound)
				if err != nil {
					return err
				}
				left -= size
				staged.Inputs = append(staged.Inputs, domain.AttestedInput{Name: in.Name, Size: size, SHA256: sum})
				st.SetFlag("input."+in.Name, fmt.Sprintf("%d sha256:%s", size, sum))
			}
			// Read-only from here: the bytes the task reads are the bytes
			// whose digest is recorded.
			if err := os.Chmod(dir, 0o555); err != nil {
				return domain.Internal(err, "cannot set the mode of %s", dir)
			}
			st.Set(keyStagedInputs, staged)
			return nil
		},
		Compensate: func(ctx context.Context, st *engine.State) error {
			return removeInputs(filepath.Join(d.Paths.InputsDir(), st.OpID))
		},
	}
}

// copyInput copies one input through its descriptor, refusing past bound.
func copyInput(ctx context.Context, in openInput, dst string, bound int64) (int64, string, error) {
	if _, err := in.File.Seek(0, io.SeekStart); err != nil {
		return 0, "", domain.Internal(err, "cannot read %s", in.Name)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, "", domain.Internal(err, "cannot create %s", dst)
	}
	hash := sha256.New()
	// One byte past the bound, to tell a file that ends there from one
	// that kept going.
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(contextReader{ctx, in.File}, bound+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	switch {
	case err != nil:
		_ = os.Remove(dst)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 0, "", ctxErr
		}
		return 0, "", domain.Internal(err, "cannot copy the input %s", in.Name)
	case n > bound:
		_ = os.Remove(dst)
		if in.Max > 0 && bound == in.Max {
			return 0, "", domain.Usage("the input %s grew past its max_size of %s while it was copied",
				in.Name, domain.ByteSize(in.Max)).
				WithHint("pass a file nothing is still writing")
		}
		return 0, "", domain.Preflight(nil, "the input %s grew past the %s of free space while it was copied",
			in.Name, domain.ByteSize(bound)).
			WithHint("pass a file nothing is still writing, and free some space under the manager's directory")
	}
	if err := os.Chmod(dst, 0o444); err != nil {
		_ = os.Remove(dst)
		return 0, "", domain.Internal(err, "cannot set the mode of %s", dst)
	}
	return n, hex.EncodeToString(hash.Sum(nil)), nil
}

// contextReader stops a long copy when the operation is cancelled.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// sweepInputs removes what killed tasks left behind: every directory under
// InputsDir but this operation's. Safe only under the deployment lock.
func (d *Deps) sweepInputs(keep string) error {
	entries, err := os.ReadDir(d.Paths.InputsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return domain.Internal(err, "cannot read %s", d.Paths.InputsDir())
	}
	for _, e := range entries {
		if e.Name() == keep {
			continue
		}
		if err := removeInputs(filepath.Join(d.Paths.InputsDir(), e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// removeInputs deletes one operation's inputs. The directory is read-only once
// filled, so it is made writable first.
func removeInputs(dir string) error {
	if err := os.Chmod(dir, 0o700); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return domain.Internal(err, "cannot remove %s", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return domain.Internal(err, "cannot remove %s", dir)
	}
	return nil
}

// leftoverInputs names the staging directories a killed task left, for doctor.
func (d *Deps) leftoverInputs() ([]string, error) {
	entries, err := os.ReadDir(d.Paths.InputsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		out = append(out, filepath.Join(d.Paths.InputsDir(), e.Name()))
	}
	return out, nil
}

// inputMounts are the staged inputs as the one-off container sees them.
func inputMounts(staged stagedInputs) ([]ports.Mount, map[string]string) {
	mounts := make([]ports.Mount, 0, len(staged.Inputs))
	env := make(map[string]string, len(staged.Inputs))
	for _, in := range staged.Inputs {
		mounts = append(mounts, ports.Mount{
			Source:   filepath.Join(staged.Dir, in.Name),
			Target:   domain.TaskInputPath(in.Name),
			ReadOnly: true,
		})
		env[domain.TaskInputEnv(in.Name)] = domain.TaskInputPath(in.Name)
	}
	return mounts, env
}
