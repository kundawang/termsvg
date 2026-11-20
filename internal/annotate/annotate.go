// Package annotate provides a time-based annotation track for terminal
// recordings. An annotation highlights a rectangle of terminal cells and
// shows a text label during a recording-time interval. Annotations are an
// overlay: they never modify the recorded frames, and a recording without
// annotations renders exactly as before.
package annotate

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// MaxAnnotations bounds the annotation track. More annotations are a
// configuration error, not something to silently drop.
const MaxAnnotations = 128

// Annotation highlights a cell rectangle and shows Text during [From, To).
// Times are recording times in seconds as the user wrote them; the IR
// processor maps them onto the playback timeline (speed, idle cap) before
// rendering. Row and Col are 0-based screen cells at display time. Rows and
// Cols give the highlight size in cells; Cols <= 0 means "to the right edge".
// An empty Text draws only the highlight.
type Annotation struct {
	From, To   time.Duration
	Row, Col   int
	Rows, Cols int
	Text       string
}

// jsonAnnotation is the on-disk form. Times are duration strings ("1.5s",
// "200ms") or plain numbers of seconds.
type jsonAnnotation struct {
	From duration `json:"from"`
	To   duration `json:"to"`
	Row  int      `json:"row"`
	Col  int      `json:"col"`
	Rows int      `json:"rows"`
	Cols int      `json:"cols"`
	Text string   `json:"text"`
}

// duration unmarshals a JSON string parsed by time.ParseDuration or a JSON
// number read as seconds.
type duration time.Duration

func (d *duration) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		parsed, err := time.ParseDuration(text)
		if err != nil {
			return fmt.Errorf("invalid duration %q (use e.g. \"1.5s\" or seconds as a number)", text)
		}
		*d = duration(parsed)
		return nil
	}
	var seconds float64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return fmt.Errorf("time must be a duration string or seconds, got %s", data)
	}
	*d = duration(seconds * float64(time.Second))
	return nil
}

// LoadFile reads and validates an annotation track. Structural problems
// (reversed or zero-length intervals, negative times, more than
// MaxAnnotations entries) are errors; problems that depend on the terminal
// size or recording duration are left to Normalize.
func LoadFile(path string) ([]Annotation, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is from user CLI input
	if err != nil {
		return nil, fmt.Errorf("reading annotations: %w", err)
	}
	return Parse(data)
}

// Parse decodes and validates an annotation track.
func Parse(data []byte) ([]Annotation, error) {
	var raw []jsonAnnotation
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing annotations: %w", err)
	}
	if len(raw) > MaxAnnotations {
		return nil, fmt.Errorf("too many annotations: %d given, at most %d allowed", len(raw), MaxAnnotations)
	}
	annotations := make([]Annotation, len(raw))
	for i, entry := range raw {
		from, to := time.Duration(entry.From), time.Duration(entry.To)
		switch {
		case from < 0 || to < 0:
			return nil, fmt.Errorf("annotation %d: times must not be negative (from %v, to %v)", i, from, to)
		case to <= from:
			return nil, fmt.Errorf("annotation %d: empty or reversed interval (from %v, to %v)", i, from, to)
		}
		annotations[i] = Annotation{
			From: from, To: to,
			Row: entry.Row, Col: entry.Col, Rows: entry.Rows, Cols: entry.Cols,
			Text: entry.Text,
		}
	}
	return annotations, nil
}

// Normalize clamps annotations to a cols x rows canvas and a recording
// duration, resolving Cols <= 0 to the right edge. Overlapping intervals are
// allowed and drawn in track order, later entries on top. Annotations that
// can never be visible are dropped; every drop or clamp is reported as a
// warning.
func Normalize(annotations []Annotation, cols, rows int, duration time.Duration) (kept []Annotation, warnings []string) {
	for i, ann := range annotations {
		// Clamp the interval to the recording.
		if ann.From >= duration {
			warnings = append(warnings, fmt.Sprintf("annotation %d: starts at %v, at or after the recording end (%v); dropped", i, ann.From, duration))
			continue
		}
		if ann.To > duration {
			warnings = append(warnings, fmt.Sprintf("annotation %d: ends at %v, past the recording end (%v); clamped", i, ann.To, duration))
			ann.To = duration
		}

		// Clamp the rectangle to the canvas.
		if ann.Rows <= 0 {
			ann.Rows = 1
		}
		if ann.Cols <= 0 {
			ann.Cols = cols - ann.Col
		}
		if ann.Row < 0 || ann.Col < 0 || ann.Row >= rows || ann.Col >= cols || ann.Cols <= 0 {
			warnings = append(warnings, fmt.Sprintf("annotation %d: cell rectangle row %d col %d is outside the %dx%d terminal; dropped", i, ann.Row, ann.Col, cols, rows))
			continue
		}
		if ann.Row+ann.Rows > rows {
			warnings = append(warnings, fmt.Sprintf("annotation %d: %d rows from row %d exceed the %d-row terminal; clamped", i, ann.Rows, ann.Row, rows))
			ann.Rows = rows - ann.Row
		}
		if ann.Col+ann.Cols > cols {
			warnings = append(warnings, fmt.Sprintf("annotation %d: %d cols from col %d exceed the %d-col terminal; clamped", i, ann.Cols, ann.Col, cols))
			ann.Cols = cols - ann.Col
		}
		kept = append(kept, ann)
	}
	return kept, warnings
}

// MapTimeline returns a function that maps recording times (seconds) onto
// playback times, given the event times before and after a timing
// transformation (speed change, idle cap). Inside a compressed gap the map
// interpolates linearly, so an annotation keeps its relative position in the
// pause; before the first and after the last event the map shifts with the
// nearest event. The map is monotonic, so valid intervals stay valid.
func MapTimeline(orig, mapped []float64) func(float64) float64 {
	return func(t float64) float64 {
		n := len(orig)
		if n == 0 {
			return t
		}
		i := sort.Search(n, func(i int) bool { return orig[i] > t }) - 1
		if i < 0 {
			return t + (mapped[0] - orig[0])
		}
		j := i + 1
		for j < n && orig[j] == orig[i] {
			j++
		}
		if j == n {
			return t + (mapped[i] - orig[i])
		}
		fraction := (t - orig[i]) / (orig[j] - orig[i])
		return mapped[i] + fraction*(mapped[j]-mapped[i])
	}
}

// Remap applies a timeline map (see MapTimeline) to every annotation.
func Remap(annotations []Annotation, mapTime func(float64) float64) []Annotation {
	if len(annotations) == 0 {
		return nil
	}
	out := make([]Annotation, len(annotations))
	for i, ann := range annotations {
		out[i] = ann
		out[i].From = time.Duration(mapTime(ann.From.Seconds()) * float64(time.Second))
		out[i].To = time.Duration(mapTime(ann.To.Seconds()) * float64(time.Second))
	}
	return out
}
