package views

import (
	"fmt"
	"io"
	"time"

	"github.com/morzecrew/morzer/internal/domain"
	"github.com/morzecrew/morzer/internal/ports"
	"github.com/morzecrew/morzer/internal/ui"
	"github.com/morzecrew/morzer/internal/ui/theme"
)

func init() {
	ui.Register(ui.View[[]ports.ServiceState]{
		Rich: func(w io.Writer, t *theme.Theme, v []ports.ServiceState) {
			emit(w, servicesDoc(doc(w, t), v))
		},
		Plain: func(w io.Writer, v []ports.ServiceState) { emit(w, servicesDoc(plainDoc(w), v)) },
	})
	ui.Register(ui.View[[]ports.ServiceStats]{
		Rich: func(w io.Writer, t *theme.Theme, v []ports.ServiceStats) {
			emit(w, StatsDoc(doc(w, t), v))
		},
		Plain: func(w io.Writer, v []ports.ServiceStats) { emit(w, StatsDoc(plainDoc(w), v)) },
	})
	ui.Register(ui.View[Sample]{
		Rich: func(w io.Writer, t *theme.Theme, v Sample) {
			emit(w, sampleDoc(doc(w, t), v))
		},
		Plain: func(w io.Writer, v Sample) { emit(w, sampleDoc(plainDoc(w), v)) },
	})
}

// Sample is one reading with the instant it was taken.
//
// A type rather than a field on the rows, for the reason `Verbose` is a type:
// whether a reading is one of a series is a presentation choice, and a
// timestamp inside the report would travel through the lifecycle layer into
// `--json` -- where a consumer looping around single-shot samples has its own
// clock and did not ask for the manager's.
//
// It exists because `stats --watch` outside a terminal *appends* rather than
// redraws, and a file holding twenty tables with nothing between them is not a
// time series.
type Sample struct {
	At    time.Time
	Stats []ports.ServiceStats
}

func sampleDoc(d *ui.Doc, s Sample) *ui.Doc {
	t := d.Theme()

	// Box drawing where the terminal has it, ASCII where it does not --
	// the Linux virtual console renders a fixed font with no box characters
	// in it, and this line's whole job is to be a legible separator.
	rule := "──"
	if t.Symbols == theme.ASCIISymbols {
		rule = "--"
	}

	d.Text(0, "%s", t.Dim(rule+" "+s.At.Format("15:04:05")+" "+rule))
	StatsDoc(d, s.Stats)
	d.Blank()
	return d
}

// servicesDoc is `morzer ps`: what this deployment is running, and nothing else.
//
// `status` draws the same slice under a heading, beside the release, the last
// backup and the lock. This is the one question on its own, because an operator
// watching a crash loop asks it repeatedly and does not want the other three
// answers each time.
func servicesDoc(d *ui.Doc, services []ports.ServiceState) *ui.Doc {
	t := d.Theme()

	rows := make([][]string, 0, len(services))
	for _, s := range services {
		symbol, style := t.Symbols.OK, t.OK
		if !s.Running() {
			symbol, style = t.Symbols.Fail, t.Fail
		}

		health := string(s.Health)
		if s.Health == ports.HealthNone || s.Health == "" {
			// No probe declared is not a verdict, and printing
			// "none" in the health column reads as one.
			health = t.Dim("-")
		}

		rows = append(rows, []string{
			style(symbol) + " " + s.Name,
			t.Dim(s.Container),
			style(s.State),
			health,
			// Shortened here rather than in the port: the digest is
			// what pins the release and the repository is what
			// identifies it, and a 71-character reference would take
			// the table apart on any terminal.
			t.Dim(domain.ShortImageRef(s.Image)),
			t.Dim(s.Status),
		})
	}

	d.Table(0, ui.Table{
		Columns: []ui.Column{
			{Header: "service", Essential: true},
			// Essential, because a scaled service is several rows
			// under one name and this is the only thing that tells
			// them apart. A listing that dropped it on a narrow
			// terminal would show two identical rows.
			{Header: "container", Essential: true},
			{Header: "state", Essential: true},
			{Header: "health"},
			{Header: "image"},
			// Last because it is the runtime's own sentence -- "Up 3
			// hours", "Exited (1) 2 minutes ago" -- and the state
			// column above already carries the verdict.
			{Header: "status"},
		},
		Rows:  rows,
		Empty: "nothing is running; `morzer apply` converges the deployment",
	})
	return d
}

// StatsDoc is one sample of resource use, one row per container.
//
// Never an aggregate row per service: `docker stats` reports containers, so a
// scaled service is several rows, and collapsing them would print one replica's
// numbers under the service's name. The total at the foot covers only the two
// figures that add.
//
// Exported and taking its document for the reason StatusDoc is: `stats --watch`
// redraws this body inside a running terminal program, and two implementations
// of "what a sample looks like" is how a live view and a printed one start
// disagreeing about the same numbers.
func StatsDoc(d *ui.Doc, stats []ports.ServiceStats) *ui.Doc {
	t := d.Theme()

	rows := make([][]string, 0, len(stats)+1)
	var totalCPU float64
	var totalMemory int64

	for _, s := range stats {
		totalCPU += s.CPUPercent
		totalMemory += s.MemoryBytes

		rows = append(rows, []string{
			s.Service,
			t.Dim(s.Container),
			fmt.Sprintf("%.2f%%", s.CPUPercent),
			memoryCell(t, s),
			ioCell(t, s.NetRxBytes, s.NetTxBytes),
			ioCell(t, s.BlockRead, s.BlockWrite),
		})
	}

	if len(stats) > 1 {
		// Memory adds and CPU percentages add; a memory *limit* does
		// not, which is why the total line stops after two columns.
		rows = append(rows, []string{
			t.Bold("total"), "",
			t.Bold(fmt.Sprintf("%.2f%%", totalCPU)),
			t.Bold(domain.ByteSize(totalMemory).String()),
			"", "",
		})
	}

	d.Table(0, ui.Table{
		Columns: []ui.Column{
			{Header: "service", Essential: true},
			// Essential for the reason this table has one row per
			// container in the first place: a scaled service is
			// several rows under one name, and dropping the instance
			// leaves two identical rows carrying different numbers.
			// An I/O column goes first — a figure nobody can place
			// is worth less than the identity that places it.
			{Header: "container", Essential: true},
			{Header: "cpu", Right: true, Essential: true},
			{Header: "memory", Right: true, Essential: true},
			{Header: "net i/o", Right: true},
			{Header: "block i/o", Right: true},
		},
		Rows:  rows,
		Empty: "nothing is running, so there is nothing using resources",
	})
	return d
}

// memoryCell is usage against the ceiling the runtime reports.
//
// The limit is shown dimmed beside the usage rather than in a column of its
// own: it is context for the number to its left, and a container with no limit
// of its own is reported by the runtime as bounded by the host's memory, which
// is a fact about the host and not about the service.
func memoryCell(t *theme.Theme, s ports.ServiceStats) string {
	used := domain.ByteSize(s.MemoryBytes).String()
	if s.MemoryLimit <= 0 {
		return used
	}
	return used + t.Dim(" / "+domain.ByteSize(s.MemoryLimit).String())
}

// ioCell draws a pair of counters, or a dash where the host does not account
// for them.
//
// A dash and never a zero. Block IO is unaccounted under a rootless daemon,
// which is an ordinary configuration -- and a container that has written
// nothing also reports zero, so printing one for the other would make an
// unanswerable question look like an idle disk.
func ioCell(t *theme.Theme, in, out *int64) string {
	if in == nil || out == nil {
		return t.Dim("-")
	}
	return domain.ByteSize(*in).String() + t.Dim(" / ") + domain.ByteSize(*out).String()
}
