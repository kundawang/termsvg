package svg

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mrmarble/termsvg/internal/annotate"
	"github.com/mrmarble/termsvg/internal/asciicast"
	"github.com/mrmarble/termsvg/internal/ir"
	"github.com/mrmarble/termsvg/internal/renderer"
)

// scrollCast is a fixed recording in which the same command is visible before
// and after the terminal scrolls: "go test ./..." is printed on row 0, six
// more lines scroll the screen twice, and the command appears again on the
// bottom row (row 4 of 5).
const scrollCast = `{"version":2,"width":20,"height":5}
[0.5,"o","go test ./..."]
[0.6,"o","\r\nline 1"]
[0.7,"o","\r\nline 2"]
[0.8,"o","\r\nline 3"]
[0.9,"o","\r\nline 4"]
[1.0,"o","\r\nline 5"]
[1.5,"o","\r\ngo test ./..."]
[2.0,"o","\r\nok"]
`

// renderAnnotated processes scrollCast with the given track and renders it.
func renderAnnotated(t *testing.T, annotations []annotate.Annotation, opts ...Option) string {
	t.Helper()
	cast, err := asciicast.Parse(strings.NewReader(scrollCast))
	if err != nil {
		t.Fatal(err)
	}
	config := ir.DefaultProcessorConfig()
	config.Annotations = annotations
	rec, err := ir.NewProcessor(config).Process(cast)
	if err != nil {
		t.Fatal(err)
	}
	rec.Annotations, _ = annotate.Normalize(rec.Annotations, rec.Width, rec.Height, rec.Duration)

	r := New(renderer.DefaultConfig(), opts...)
	var buf bytes.Buffer
	if err := r.Render(context.Background(), rec, &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// TestAnnotation_BoxPositionAfterScroll checks that an annotation anchored to
// the second occurrence of the scrolled command is drawn over that
// occurrence's cells: row 4, columns 0-12 of the 20x5 canvas.
func TestAnnotation_BoxPositionAfterScroll(t *testing.T) {
	out := renderAnnotated(t, []annotate.Annotation{
		{From: 1500 * time.Millisecond, To: 1800 * time.Millisecond, Row: 4, Col: 0, Rows: 1, Cols: 13, Text: "same command"},
	})

	// Row 4 * 25px = y 100; 13 cols * 12px = width 156.
	if !strings.Contains(out, `<rect class="annh" x="0" y="100" width="156" height="25"/>`) {
		t.Errorf("highlight rect missing or misplaced in:\n%s", out)
	}
	// The label does not fit below row 4, so it sits above it, on row 3.
	if !strings.Contains(out, `<rect class="annb" x="0" y="75" width="152" height="25" rx="3"/>`) {
		t.Errorf("label plate missing or misplaced in:\n%s", out)
	}
	if !strings.Contains(out, `<text class="annt" x="4" y="95">same command</text>`) {
		t.Errorf("label text missing or misplaced in:\n%s", out)
	}
}

// TestAnnotation_AppearanceTime checks the visibility keyframes against the
// recording timeline: the annotation covers 1.5s..1.8s of the 2s recording,
// i.e. 75%..90%.
func TestAnnotation_AppearanceTime(t *testing.T) {
	out := renderAnnotated(t, []annotate.Annotation{
		{From: 1500 * time.Millisecond, To: 1800 * time.Millisecond, Row: 4, Col: 0, Rows: 1, Cols: 13, Text: "note"},
	})

	if !strings.Contains(out, `@keyframes ann0{0%{visibility:hidden}75%{visibility:visible}90%{visibility:hidden}100%{visibility:hidden}}`) {
		t.Errorf("visibility keyframes missing or wrong in:\n%s", out)
	}
	if !strings.Contains(out, `<g style="animation:ann0 2s infinite step-end" visibility="hidden">`) {
		t.Errorf("annotation group missing its animation in:\n%s", out)
	}
}

// TestAnnotation_UnchangedOutputWithoutTrack pins requirement: a recording
// without annotations renders exactly as before the feature existed.
func TestAnnotation_UnchangedOutputWithoutTrack(t *testing.T) {
	plain := renderAnnotated(t, nil)
	empty := renderAnnotated(t, []annotate.Annotation{})

	if plain != empty {
		t.Error("an empty annotation track changed the output")
	}
	for _, marker := range []string{"annh", "annb", "annt", "@keyframes ann"} {
		if strings.Contains(plain, marker) {
			t.Errorf("output without annotations contains %q", marker)
		}
	}
}

func TestAnnotation_OverlappingIntervals(t *testing.T) {
	out := renderAnnotated(t, []annotate.Annotation{
		{From: 500 * time.Millisecond, To: 1600 * time.Millisecond, Row: 0, Col: 0, Rows: 1, Cols: 5, Text: "first"},
		{From: 1500 * time.Millisecond, To: 2 * time.Second, Row: 4, Col: 0, Rows: 1, Cols: 5, Text: "second"},
	})

	if !strings.Contains(out, "@keyframes ann0{") || !strings.Contains(out, "@keyframes ann1{") {
		t.Errorf("overlapping annotations should each get keyframes:\n%s", out)
	}
	if strings.Index(out, ">first</text>") > strings.Index(out, ">second</text>") {
		t.Error("annotations should be drawn in track order, later on top")
	}
}

func TestAnnotation_SMIL(t *testing.T) {
	options := DefaultOptions()
	options.Animation = AnimationSMIL
	out := renderAnnotated(t, []annotate.Annotation{
		{From: 1500 * time.Millisecond, To: 1800 * time.Millisecond, Row: 4, Col: 0, Rows: 1, Cols: 13},
	}, WithOptions(options))

	if !strings.Contains(out, `<animate attributeName="visibility" values="hidden;visible;hidden;hidden" keyTimes="0;.75;.9;1"`) {
		t.Errorf("SMIL visibility animation missing in:\n%s", out)
	}
	if !strings.Contains(out, `<rect class="annh" x="0" y="100" width="156" height="25"/>`) {
		t.Errorf("SMIL export lost the highlight rect:\n%s", out)
	}
}

func TestAnnotation_Minified(t *testing.T) {
	config := renderer.DefaultConfig()
	config.Minify = true
	cast, err := asciicast.Parse(strings.NewReader(scrollCast))
	if err != nil {
		t.Fatal(err)
	}
	irConfig := ir.DefaultProcessorConfig()
	irConfig.Annotations = []annotate.Annotation{
		{From: 1500 * time.Millisecond, To: 1800 * time.Millisecond, Row: 4, Col: 0, Rows: 1, Cols: 13, Text: "same command"},
	}
	rec, err := ir.NewProcessor(irConfig).Process(cast)
	if err != nil {
		t.Fatal(err)
	}
	rec.Annotations, _ = annotate.Normalize(rec.Annotations, rec.Width, rec.Height, rec.Duration)

	var buf bytes.Buffer
	if err := New(config).Render(context.Background(), rec, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `class="annh"`) || !strings.Contains(out, "same command") {
		t.Errorf("minified export lost the annotation:\n%s", out)
	}
}
