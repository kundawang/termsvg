package play

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mrmarble/termsvg/cmd/termsvg/internal/pacing"
	"github.com/mrmarble/termsvg/internal/annotate"
	"github.com/mrmarble/termsvg/internal/asciicast"
	"github.com/mrmarble/termsvg/internal/color"
	"github.com/mrmarble/termsvg/internal/terminal"
)

type Cmd struct {
	File string `arg:"" type:"existingfile" help:"Asciicast file to play"`

	Annotate string `type:"existingfile" help:"JSON annotation track to overlay during playback"`

	pacing.Flags `embed:""`
}

func (cmd *Cmd) Run() error {
	cast, err := asciicast.ParseFile(cmd.File)
	if err != nil {
		return err
	}

	annotations, err := loadAnnotations(cmd.Annotate)
	if err != nil {
		return err
	}

	return playback(cast, cmd.Speed, cast.IdleLimit(cmd.MaxIdle), time.Sleep, os.Stdout, annotations)
}

// loadAnnotations reads the annotation track named by path; an empty path
// means no track.
func loadAnnotations(path string) ([]annotate.Annotation, error) {
	if path == "" {
		return nil, nil
	}
	return annotate.LoadFile(path)
}

// playback prints the output events at their speed-adjusted times, waiting
// with sleep, and overlays the annotation track on the terminal while it is
// visible. The overlay is drawn around the replayed stream: a screen model
// follows the events so a highlight can be erased by reprinting the cells it
// covered. The recording itself is replayed byte for byte, annotations or
// not.
func playback(
	cast *asciicast.Cast,
	speed float64,
	maxIdle time.Duration,
	sleep func(time.Duration),
	out io.Writer,
	annotations []annotate.Annotation,
) error {
	orig := eventTimes(cast)

	cast.ToRelativeTime()

	if maxIdle > 0 {
		cast.CapRelativeTime(maxIdle.Seconds())
	}

	cast.ToAbsoluteTime()
	cast.AdjustSpeed(speed)

	annotations = annotate.Remap(annotations, annotate.MapTimeline(orig, eventTimes(cast)))

	if len(annotations) > 0 {
		cols, rows := maxScreenSize(cast)
		duration := time.Duration(cast.Events[len(cast.Events)-1].Time * float64(time.Second))
		var warnings []string
		annotations, warnings = annotate.Normalize(annotations, cols, rows, duration)
		for _, warning := range warnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
		}
	}

	player := newOverlayPlayer(out, cast, annotations)

	startTime := time.Now()
	wait := func(target time.Duration) {
		if delay := target - time.Since(startTime); delay > 0 {
			sleep(delay)
		}
	}

	for _, event := range cast.Events {
		target := time.Duration(event.Time * float64(time.Second))
		// Annotation changes due at or before this event are applied first, so
		// an annotation is visible exactly over its [From, To) interval.
		player.applyDue(target, wait)

		switch event.EventType {
		case asciicast.Output:
			wait(target)
			fmt.Fprint(out, event.EventData)
			player.feed(event.EventData)
		case asciicast.Resize:
			// Resizes are not replayed to the terminal, but the screen model
			// follows them so later highlights land on the right cells.
			player.resize(event.EventData)
		}
	}

	// Annotations may outlive the last event.
	player.applyDue(time.Duration(1<<62), wait)

	return nil
}

func eventTimes(cast *asciicast.Cast) []float64 {
	times := make([]float64, len(cast.Events))
	for i, event := range cast.Events {
		times[i] = event.Time
	}
	return times
}

// maxScreenSize returns the largest terminal size in the recording, so
// annotation bounds follow the same canvas as an export would.
func maxScreenSize(cast *asciicast.Cast) (cols, rows int) {
	cols, rows = cast.Header.Width, cast.Header.Height
	for _, event := range cast.Events {
		if event.EventType != asciicast.Resize {
			continue
		}
		var c, r int
		if _, err := fmt.Sscanf(strings.TrimSpace(event.EventData), "%dx%d", &c, &r); err == nil {
			cols, rows = max(cols, c), max(rows, r)
		}
	}
	return cols, rows
}

// overlayAction shows or hides one annotation at a playback instant.
type overlayAction struct {
	time  time.Duration
	index int
	show  bool
}

// overlayPlayer draws the annotation track onto the terminal around the
// replayed output. It is a no-op when the track is empty.
type overlayPlayer struct {
	out         io.Writer
	annotations []annotate.Annotation
	actions     []overlayAction
	next        int
	term        *terminal.Emulator
	active      map[int]bool
	drawn       map[[2]int]bool
}

func newOverlayPlayer(out io.Writer, cast *asciicast.Cast, annotations []annotate.Annotation) *overlayPlayer {
	player := &overlayPlayer{out: out, annotations: annotations, active: map[int]bool{}, drawn: map[[2]int]bool{}}
	if len(annotations) == 0 {
		return player
	}
	player.term = terminal.New(cast.Header.Width, cast.Header.Height)
	player.actions = make([]overlayAction, 0, 2*len(annotations))
	for i, ann := range annotations {
		player.actions = append(player.actions,
			overlayAction{time: ann.From, index: i, show: true},
			overlayAction{time: ann.To, index: i, show: false})
	}
	// Hides sort before shows at the same instant, matching [From, To).
	sort.SliceStable(player.actions, func(i, j int) bool {
		a, b := player.actions[i], player.actions[j]
		if a.time != b.time {
			return a.time < b.time
		}
		return !a.show && b.show
	})
	return player
}

// applyDue applies every annotation change due at or before target, waiting
// for each one's instant.
func (p *overlayPlayer) applyDue(target time.Duration, wait func(time.Duration)) {
	for p.next < len(p.actions) && p.actions[p.next].time <= target {
		wait(p.actions[p.next].time)
		action := p.actions[p.next]
		p.active[action.index] = action.show
		p.redraw()
		p.next++
	}
}

func (p *overlayPlayer) feed(data string) {
	if p.term != nil {
		_, _ = p.term.Write([]byte(data))
	}
}

func (p *overlayPlayer) resize(data string) {
	if p.term == nil {
		return
	}
	var cols, rows int
	if _, err := fmt.Sscanf(data, "%dx%d", &cols, &rows); err == nil {
		p.term.Resize(cols, rows)
	}
}

// redraw erases the previously drawn overlay and draws the currently active
// annotations, all around a saved cursor so the replayed stream is
// undisturbed.
func (p *overlayPlayer) redraw() {
	if p.term == nil {
		return
	}
	var sb strings.Builder
	sb.WriteString("\x1b7") // save cursor
	for cell := range p.drawn {
		writeCell(&sb, p.term, cell[0], cell[1], false)
	}
	p.drawn = map[[2]int]bool{}
	for i, ann := range p.annotations {
		if !p.active[i] {
			continue
		}
		p.drawHighlight(&sb, ann)
		p.drawLabel(&sb, ann)
	}
	sb.WriteString("\x1b8") // restore cursor
	fmt.Fprint(p.out, sb.String())
}

// drawHighlight reverses every cell of the annotation rectangle that is on
// the current screen. Cells are addressed one by one, so wide characters and
// resized screens cannot shift the rectangle.
func (p *overlayPlayer) drawHighlight(sb *strings.Builder, ann annotate.Annotation) {
	for row := ann.Row; row < ann.Row+max(ann.Rows, 1) && row < p.term.Height(); row++ {
		if row < 0 {
			continue
		}
		for col := ann.Col; col < ann.Col+ann.Cols && col < p.term.Width(); col++ {
			if col < 0 {
				continue
			}
			writeCell(sb, p.term, col, row, true)
			p.drawn[[2]int{col, row}] = true
		}
	}
}

// drawLabel prints the annotation text below the highlight, or above it when
// there is no room below, in reverse video.
func (p *overlayPlayer) drawLabel(sb *strings.Builder, ann annotate.Annotation) {
	if ann.Text == "" {
		return
	}
	row := ann.Row + max(ann.Rows, 1)
	if row >= p.term.Height() {
		row = ann.Row - 1
	}
	if row < 0 || row >= p.term.Height() {
		return
	}
	col := max(ann.Col, 0)
	for _, r := range ann.Text {
		if col >= p.term.Width() {
			break
		}
		fmt.Fprintf(sb, "\x1b[%d;%dH\x1b[0;7m%c", row+1, col+1, r)
		p.drawn[[2]int{col, row}] = true
		col++
	}
}

// writeCell reprints one screen cell from the model, reversed for a highlight
// or with its own attributes to erase one.
func writeCell(sb *strings.Builder, term *terminal.Emulator, col, row int, reverse bool) {
	cell := term.Cell(col, row)
	char := cell.Char
	if char == 0 {
		char = ' '
	}
	fmt.Fprintf(sb, "\x1b[%d;%dH%s", row+1, col+1, cellSGR(cell, reverse))
	sb.WriteRune(char)
}

// cellSGR builds the SGR sequence reproducing a cell's colors and attributes.
func cellSGR(cell terminal.Cell, reverse bool) string {
	var sb strings.Builder
	sb.WriteString("\x1b[0m")
	if reverse {
		sb.WriteString("\x1b[7m")
	}
	if cell.Bold {
		sb.WriteString("\x1b[1m")
	}
	if cell.Italic {
		sb.WriteString("\x1b[3m")
	}
	if cell.Underline {
		sb.WriteString("\x1b[4m")
	}
	writeColorSGR(&sb, cell.Foreground, 30, 38)
	writeColorSGR(&sb, cell.Background, 40, 48)
	return sb.String()
}

// writeColorSGR emits the SGR sequence for one color. base is the ANSI
// foreground/background base (30/40) and extended the 8-bit selector (38/48).
func writeColorSGR(sb *strings.Builder, c color.Color, base, extended int) {
	switch c.Type {
	case color.ANSI:
		if c.Index < 8 {
			fmt.Fprintf(sb, "\x1b[%dm", base+int(c.Index))
		} else {
			fmt.Fprintf(sb, "\x1b[%dm", base+60+int(c.Index)-8)
		}
	case color.Extended:
		fmt.Fprintf(sb, "\x1b[%d;5;%dm", extended, c.Index)
	case color.TrueColor:
		fmt.Fprintf(sb, "\x1b[%d;2;%d;%d;%dm", extended, c.RGB.R, c.RGB.G, c.RGB.B)
	case color.Default:
		// The reset already restored the default color.
	}
}
