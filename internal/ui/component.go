package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/morzecrew/morzer/internal/ui/theme"
)

// The five components every view is built from -- heading, fields, table,
// checks, callout -- and the three primitives that place them: a title, a
// wrapped paragraph, and a blank line.
//
// The consistency the output lacked was never a style guide, it was a
// vocabulary: `secret list`, `release list`, `backup list` and `config list`
// were four hand-rolled printf tables with four hard-coded width constants, and
// the 28 in `%-28s` was a bug waiting for a 30-character secret name. One
// implementation whose widths come from the data replaces all four.
//
// A sixth component is a normal change to this package. What is not normal is a
// view drawing its own layout inline, which is how the four incompatible tables
// happened in the first place -- and the reason `Text` wraps to the measure
// rather than taking a pre-padded string is that a primitive which accepted one
// would be that same escape hatch with a shorter name.

// Doc is one view's output.
//
// It carries the mode rather than making each component take it, so a view
// writes the same calls in both renderings and cannot accidentally style one
// and not the other. Plain is not "rich without colour": it is line-oriented
// and stable in a journal, so it drops borders and keeps words where rich uses
// weight. Where the two legitimately differ, the difference is here and not in
// fifteen views.
type Doc struct {
	b strings.Builder
	t *theme.Theme

	// width is the measure: how far wrapped text runs, capped at
	// MaxContentWidth however wide the screen is.
	width int

	// screen is the terminal. Only one thing may consult it -- a table
	// deciding whether all its columns fit -- because that is the only
	// legitimate use of extra width: it buys columns, never padding.
	screen Screen

	plain bool

	// started is how Heading knows whether it owes a blank line above it:
	// a section separator before the first section would be a leading blank
	// line in a journal.
	started bool
}

// NewDoc starts a styled document on a screen of the given width.
//
// The viewport goes in and the measure is derived, rather than both being
// passed: a caller that could set them independently is a caller that can
// justify content to the screen, which is the defect this whole component set
// exists to remove.
func NewDoc(t *theme.Theme, screen Screen) *Doc {
	return &Doc{t: t, width: measureFor(screen.Width), screen: screen}
}

// NewPlainDoc starts a line-oriented one.
//
// The theme is the colourless ASCII one rather than nil, so every component
// calls a style unconditionally and the monochrome path cannot rot behind a
// branch nobody takes -- the same reasoning theme.New already applies to
// NO_COLOR.
func NewPlainDoc(screen Screen) *Doc {
	return &Doc{
		t:      theme.New(false, false),
		width:  measureFor(screen.Width),
		screen: screen,
		plain:  true,
	}
}

// minContentWidth is the narrowest measure anything is drawn at.
//
// A Doc is a public type and a Screen is a plain struct, so a zero can arrive
// from a caller that has not been told a width yet -- the live view is handed
// one by Bubble Tea and starts at nothing. A measure of zero wraps every line
// to itself and makes every table too narrow for any column, which is a first
// frame that looks like a bug in the report rather than in the sizing.
const minContentWidth = 20

func measureFor(viewport int) int {
	switch {
	case viewport < minContentWidth:
		return minContentWidth
	case viewport < MaxContentWidth:
		return viewport
	default:
		return MaxContentWidth
	}
}

// String is the assembled document.
func (d *Doc) String() string { return d.b.String() }

// Emit puts the document on a stream.
//
// Not WriteTo: that name belongs to io.WriterTo, whose contract returns a count
// and an error, and a view has nothing to do with either -- a presentation
// fault must never be how a command fails.
func (d *Doc) Emit(w io.Writer) { _, _ = io.WriteString(w, d.b.String()) }

// Theme is the styling in force, for the few places a view needs a symbol.
func (d *Doc) Theme() *theme.Theme { return d.t }

// line appends one rendered line.
func (d *Doc) line(s string) {
	d.b.WriteString(strings.TrimRight(s, " "))
	d.b.WriteByte('\n')
	d.started = true
}

// Blank appends a separator line.
//
// Used between sections and never between rows: vertical rhythm is what makes a
// report scannable, and a blank line inside a table destroys the alignment that
// is the only reason to draw one.
func (d *Doc) Blank() {
	if !d.started {
		return
	}
	d.line("")
}

// Title is the document's own name, drawn once at the top.
func (d *Doc) Title(text string) {
	d.line(d.t.Bold(text))
}

// Heading opens a section.
//
// The spacing rule lives here rather than at each call site: one blank line
// above, none below, and none at all when this is the first thing in the
// document.
func (d *Doc) Heading(text string) {
	d.Blank()
	d.line("  " + d.t.Bold(text))
}

// Verbatim writes one line exactly as given, past the measure if it must.
//
// For a value that exists to be copied: an age public key is 62 characters and
// a narrow terminal is 60, and `key=$(morzer secret recipients
// generate-recovery-key ./k)` captures whatever is printed. The measure governs
// prose, which is meant to be read; half of a key is not a narrower key, it is
// a broken one.
func (d *Doc) Verbatim(s string) { d.line(s) }

// Text writes a paragraph, wrapped inside the measure.
func (d *Doc) Text(indent int, format string, args ...any) {
	prefix := strings.Repeat(" ", indent)
	for _, l := range Wrap(fmt.Sprintf(format, args...), d.width-indent) {
		d.line(prefix + l)
	}
}

// ----------------------------------------------------------------------------
// Fields

// Field is one label/value pair.
type Field struct {
	Label string
	Value string
	// Note trails the value in the dim role -- "(not installed)", "-- a
	// sandbox". Separate from Value so the value stays the thing a reader
	// copies.
	Note string
}

// Fields draws a label/value block: labels padded to the longest, values
// wrapped inside the measure and aligned under themselves.
//
// The wrapping is what makes this a component rather than a printf. A support
// URL or an error message longer than the measure used to run off the right of
// the screen; here it breaks and the continuation lines up under the value,
// which is the only arrangement in which a two-line value still reads as one
// field.
func (d *Doc) Fields(indent int, fields []Field) {
	label := 0
	for _, f := range fields {
		if n := Width(f.Label); n > label {
			label = n
		}
	}
	d.FieldsPadded(indent, label, fields)
}

// FieldsPadded is Fields with the label column given rather than computed.
//
// For a view whose fields are emitted in more than one call and must still line
// up -- `doctor`'s collapsed groups are interleaved with expanded ones, and a
// label column computed per call would step in and out by a character per
// group.
func (d *Doc) FieldsPadded(indent, label int, fields []Field) {
	prefix := strings.Repeat(" ", indent)
	// The gutter is the same everywhere; the continuation indent is the
	// label column plus it, so a wrapped value sits under the first line of
	// itself rather than under the label.
	hanging := prefix + strings.Repeat(" ", label+Gutter)
	room := d.width - Width(hanging)

	for _, f := range fields {
		value := f.Value
		if f.Note != "" {
			value += " " + d.t.Dim(f.Note)
		}

		// Wrap always yields at least one line, including for an empty
		// value: a field with nothing in it is still a labelled row,
		// because "unset" and "absent" are different answers.
		lines := Wrap(value, room)
		d.line(prefix + pad(d.t.Dim(f.Label), label+Gutter) + lines[0])
		for _, l := range lines[1:] {
			d.line(hanging + l)
		}
	}
}

// ----------------------------------------------------------------------------
// Table

// Column declares one column of a table.
type Column struct {
	Header string
	// Essential columns are never dropped. A table too narrow for its
	// essential columns is drawn anyway and overflows, because dropping the
	// name of the thing each row is about would leave rows nothing
	// identifies.
	Essential bool
	// Right aligns the column's cells to the right, for counts and sizes.
	Right bool
}

// Table is a header row and its data, with the widths computed from the data.
//
// Narrow terminals degrade by dropping the columns declared inessential, in
// reverse declaration order, and a footer says which. They never wrap a cell:
// a wrapped cell destroys the alignment that is the only reason to use a table,
// and "the last column is missing, and it says so" is a decision where "every
// row is now two ragged lines" is an accident.
type Table struct {
	Columns []Column
	Rows    [][]string
	// Empty is what to say instead of a header with nothing under it.
	Empty string
	// NoHeader draws the rows alone. For the short lists inside a larger
	// report -- services under a `services` heading -- where the heading
	// has already said what the columns are and a header row would be a
	// second one.
	NoHeader bool
}

// Table draws one.
func (d *Doc) Table(indent int, tbl Table) {
	if len(tbl.Rows) == 0 {
		if tbl.Empty != "" {
			d.Text(indent, "%s", d.t.Dim(tbl.Empty))
		}
		return
	}

	// Seeded from the headers only when they are drawn. A suppressed
	// header still setting the column width is how `ok` ended up padded to
	// the six columns of a RESULT nobody could see.
	// No single column may be wider than the measure. A table may exceed
	// the measure in total -- one carrying a digest and a path legitimately
	// needs 130 columns -- but one cell that does is not a wide table, it is
	// a paragraph in a column: an unreachable target's error is a sentence,
	// and left whole it makes one row as long as the report.
	cap := d.width
	widths := make([]int, len(tbl.Columns))
	if !tbl.NoHeader {
		for i, c := range tbl.Columns {
			widths[i] = Width(c.Header)
		}
	}
	for _, row := range tbl.Rows {
		for i, cell := range row {
			if i >= len(widths) {
				continue
			}
			if n := Width(cell); n > widths[i] {
				widths[i] = min(n, cap)
			}
		}
	}

	shown, dropped := d.fit(tbl.Columns, widths, indent)
	d.shrink(shown, widths, indent)

	prefix := strings.Repeat(" ", indent)
	gutter := strings.Repeat(" ", Gutter)

	cells := make([]string, 0, len(shown))
	if !tbl.NoHeader {
		for _, i := range shown {
			cells = append(cells, align(tbl.Columns[i], strings.ToUpper(tbl.Columns[i].Header), widths[i]))
		}
		d.line(prefix + d.t.Dim(strings.Join(cells, gutter)))
	}

	for _, row := range tbl.Rows {
		cells = cells[:0]
		for _, i := range shown {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			cell = Truncate(cell, widths[i], d.ellipsis())
			cells = append(cells, align(tbl.Columns[i], cell, widths[i]))
		}
		d.line(prefix + strings.Join(cells, gutter))
	}

	if len(dropped) > 0 {
		names := make([]string, 0, len(dropped))
		for _, i := range dropped {
			names = append(names, strings.ToLower(tbl.Columns[i].Header))
		}
		d.Text(indent, "%s", d.t.Dim(fmt.Sprintf(
			"(%s hidden: the terminal is too narrow)", strings.Join(names, ", "))))
	}
}

// fit decides which columns are drawn at this width.
//
// Inessential columns are dropped from the right, which is the order they were
// declared in and therefore the order of decreasing importance the view chose.
// The alternative -- dropping the widest -- would make the same table have a
// different shape on two machines for reasons no reader could infer.
func (d *Doc) fit(columns []Column, widths []int, indent int) (shown, dropped []int) {
	shown = make([]int, 0, len(columns))
	for i := range columns {
		shown = append(shown, i)
	}
	if !d.screen.Known {
		// Nothing reported a width, so there is no screen to be too
		// narrow for this. Dropping a column against the fallback would
		// hide data in a pipe -- exactly where it was never going to be
		// truncated -- and `morzer release list | grep` losing the path
		// column is a worse answer than a long line.
		return shown, nil
	}

	room := d.screen.Width - indent
	total := func() int {
		n := Gutter * (len(shown) - 1)
		for _, i := range shown {
			n += widths[i]
		}
		return n
	}

	for total() > room && len(shown) > 1 {
		last := -1
		for at, i := range shown {
			if !columns[i].Essential {
				last = at
			}
		}
		if last < 0 {
			break
		}
		dropped = append(dropped, shown[last])
		shown = append(shown[:last], shown[last+1:]...)
	}
	return shown, dropped
}

// ellipsis marks a cell this terminal had to cut short.
func (d *Doc) ellipsis() string {
	if d.t.Symbols == theme.ASCIISymbols {
		return "..."
	}
	return "…"
}

// minColumnWidth is the narrowest a column is squeezed to before the table
// gives up and overflows.
//
// Twelve, because a cell cut below that says nothing an operator can act on: a
// truncated identifier they cannot match and a truncated sentence they cannot
// read are both worse than a row that runs long.
const minColumnWidth = 12

// shrink takes an overflow off the widest column rather than off the row's
// right edge.
//
// Dropping a column is the first answer and this is the second, for the case
// dropping cannot reach: one cell carrying a sentence -- an unreachable
// target's error -- makes a two-column table wider than any screen, and every
// other column then loses its place to a paragraph. The widest column is the
// one that stopped being a column, so it is the one that gives way.
func (d *Doc) shrink(shown []int, widths []int, indent int) {
	if !d.screen.Known {
		return
	}

	room := d.screen.Width - indent - Gutter*(len(shown)-1)
	total := func() int {
		n := 0
		for _, i := range shown {
			n += widths[i]
		}
		return n
	}

	for total() > room {
		widest, at := 0, -1
		for _, i := range shown {
			if widths[i] > widest {
				widest, at = widths[i], i
			}
		}
		if at < 0 || widths[at] <= minColumnWidth {
			return
		}
		widths[at] = max(minColumnWidth, widths[at]-(total()-room))
	}
}

func align(c Column, cell string, width int) string {
	if c.Right {
		return padLeft(cell, width)
	}
	return pad(cell, width)
}

// ----------------------------------------------------------------------------
// Checks

// CheckState is the verdict a check row carries.
type CheckState int

// The three verdicts, in the order they escalate.
const (
	CheckPassed CheckState = iota
	CheckWarned
	CheckFailed
)

// CheckRow is one diagnostic.
type CheckRow struct {
	State       CheckState
	Description string
	Message     string
}

// Checks draws diagnostics: marker, description, and the message beneath.
//
// Beneath rather than beside, and that is the whole point of the component. The
// table this replaces computed `description = width - width/3 - 12`, so the
// description column *was* the terminal and the message ended up at the far
// margin -- 20 spaces from its check at 100 columns, 87 at 200, 207 at 380. A
// message on its own indented line is the same information at every width.
func (d *Doc) Checks(indent int, rows []CheckRow) {
	prefix := strings.Repeat(" ", indent)
	for _, row := range rows {
		marker, style := d.marker(row.State)

		// Hanging past the marker, for the description as well as the
		// message: a description that wraps back to the marker column
		// reads as a second check, which is worse than the overflow it
		// was avoiding.
		hanging := prefix + strings.Repeat(" ", Width(marker)+1)
		room := d.width - Width(hanging)

		for i, l := range Wrap(row.Description, room) {
			if i == 0 {
				d.line(prefix + style(marker) + " " + l)
				continue
			}
			d.line(hanging + l)
		}
		// Guarded rather than looped over: Wrap("") yields one empty
		// line, so an unguarded loop puts a blank line under every check
		// that has nothing more to say -- which is most of them.
		if row.Message == "" {
			continue
		}
		for _, l := range Wrap(row.Message, room) {
			d.line(hanging + d.t.Dim(l))
		}
	}
}

// marker is the symbol and role for a verdict.
//
// Both, always. Colour is never the only carrier: a symbol survives NO_COLOR, a
// monochrome console, a colour-blind reader and a pipe into a file, and the
// summary line carries the word as well.
func (d *Doc) marker(state CheckState) (string, func(string) string) {
	switch state {
	case CheckFailed:
		return d.t.Symbols.Fail, d.t.Fail
	case CheckWarned:
		return d.t.Symbols.Warn, d.t.Warn
	default:
		return d.t.Symbols.OK, d.t.OK
	}
}

// ----------------------------------------------------------------------------
// Callout

// Callout is a block the operator must act on or keep.
type Callout struct {
	Title string
	Body  []string
}

// Callout draws one: bordered in rich, a prefixed block in plain.
//
// It exists because of one moment in the product. `secret generate-recovery-key`
// prints the only copy of the key that can recover a lost machine, and it had
// less visual weight than a progress line. A border is not decoration there; it
// is the difference between text an operator scrolls past and text they copy.
//
// Plain gets no border, because a box drawn in a journal is noise that outlives
// the terminal that wanted it.
func (d *Doc) Callout(indent int, c Callout) {
	prefix := strings.Repeat(" ", indent)

	if d.plain {
		d.Blank()
		d.line(prefix + strings.ToUpper(c.Title) + ":")
		for _, para := range c.Body {
			for _, l := range Wrap(para, d.width-indent-2) {
				d.line(prefix + "  " + l)
			}
		}
		return
	}

	// Sized to the content rather than to the screen, capped by the
	// measure: a box stretched to 380 columns is the same defect as a table
	// stretched to 380 columns.
	inner := 0
	room := d.width - indent - 4
	wrapped := make([][]string, 0, len(c.Body))
	for _, para := range c.Body {
		lines := Wrap(para, room)
		wrapped = append(wrapped, lines)
		for _, l := range lines {
			if n := Width(l); n > inner {
				inner = n
			}
		}
	}
	// The title has to fit in the top rule with a dash and a space either
	// side of it, or the rule it sits in is shorter than the box.
	if n := Width(c.Title) + 4; n > inner {
		inner = n
	}

	// Every line of the box is inner+4 visible columns: a corner, a rule or
	// a space, the content, a space, a corner. Written once, as arithmetic
	// against one number, because the first version computed the top rule
	// independently and drew it two columns short of the bottom one.
	set := d.boxSet()
	fill := strings.Repeat(set.rule, inner-Width(c.Title)-1)

	d.Blank()
	d.line(prefix + d.t.Highlight(
		set.topLeft+set.rule+" "+c.Title+" "+fill+set.topRight))
	for i, lines := range wrapped {
		if i > 0 {
			d.line(prefix + d.t.Highlight(set.side) + " " +
				strings.Repeat(" ", inner) + " " + d.t.Highlight(set.side))
		}
		for _, l := range lines {
			d.line(prefix + d.t.Highlight(set.side) + " " + pad(l, inner) +
				" " + d.t.Highlight(set.side))
		}
	}
	d.line(prefix + d.t.Highlight(
		set.bottomLeft+strings.Repeat(set.rule, inner+2)+set.bottomRight))
}

// boxSet is the border alphabet for this terminal.
//
// Box drawing where the terminal has it, ASCII where it does not. The Linux
// virtual console renders a fixed 512-glyph font with no box characters in it,
// and a callout made of replacement glyphs is louder than the thing it frames --
// which is the opposite of the point.
type boxSet struct {
	topLeft, topRight, bottomLeft, bottomRight, side, rule string
}

func (d *Doc) boxSet() boxSet {
	if d.t.Symbols == theme.ASCIISymbols {
		return boxSet{"+", "+", "+", "+", "|", "-"}
	}
	return boxSet{"╭", "╮", "╰", "╯", "│", "─"}
}
