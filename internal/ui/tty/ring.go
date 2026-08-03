package tty

// ring is a fixed-size buffer of the most recent lines.
//
// Subprocess output is unbounded -- an image pull emits thousands of lines --
// and the view shows the last few. Keeping only those means a long step costs
// the same memory as a short one.
type ring struct {
	lines []string
	size  int
}

func newRing(size int) *ring { return &ring{size: size} }

func (r *ring) push(line string) {
	if line == "" {
		return
	}
	r.lines = append(r.lines, line)
	if len(r.lines) > r.size {
		r.lines = r.lines[len(r.lines)-r.size:]
	}
}

func (r *ring) reset() { r.lines = nil }

func (r *ring) all() []string { return r.lines }
