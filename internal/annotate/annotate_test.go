package annotate

import (
	"strings"
	"testing"
	"time"
)

func TestParse_DurationForms(t *testing.T) {
	annotations, err := Parse([]byte(`[
		{"from": "1.5s", "to": "2500ms", "row": 1, "col": 2, "rows": 1, "cols": 3, "text": "note"},
		{"from": 3, "to": 4.5, "row": 0, "col": 0, "cols": 5}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(annotations) != 2 {
		t.Fatalf("got %d annotations, want 2", len(annotations))
	}
	if annotations[0].From != 1500*time.Millisecond || annotations[0].To != 2500*time.Millisecond {
		t.Errorf("string durations parsed as %v..%v", annotations[0].From, annotations[0].To)
	}
	if annotations[1].From != 3*time.Second || annotations[1].To != 4500*time.Millisecond {
		t.Errorf("number durations parsed as %v..%v", annotations[1].From, annotations[1].To)
	}
	if annotations[1].Text != "" {
		t.Errorf("missing text = %q, want empty (highlight only)", annotations[1].Text)
	}
}

func TestParse_Errors(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"reversed interval", `[{"from":"2s","to":"1s","row":0,"col":0,"cols":1}]`, "reversed"},
		{"zero length", `[{"from":"1s","to":"1s","row":0,"col":0,"cols":1}]`, "empty or reversed"},
		{"negative time", `[{"from":"-1s","to":"1s","row":0,"col":0,"cols":1}]`, "negative"},
		{"bad duration", `[{"from":"soon","to":"1s","row":0,"col":0,"cols":1}]`, "invalid duration"},
		{"bad type", `[{"from":[1],"to":"1s","row":0,"col":0,"cols":1}]`, "duration string or seconds"},
		{"not json", `nope`, "parsing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.json))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestParse_TooMany(t *testing.T) {
	var entries []string
	for range MaxAnnotations + 1 {
		entries = append(entries, `{"from":"0s","to":"1s","row":0,"col":0,"cols":1}`)
	}
	_, err := Parse([]byte("[" + strings.Join(entries, ",") + "]"))
	if err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("error = %v, want a too-many-annotations error", err)
	}
}

func TestNormalize_ClampsAndDrops(t *testing.T) {
	annotations := []Annotation{
		{From: 0, To: time.Second, Row: 1, Col: 1, Rows: 1, Cols: 3, Text: "ok"},
		{From: 0, To: time.Second, Row: 9, Col: 0, Rows: 1, Cols: 1},                   // row out of bounds
		{From: 0, To: time.Second, Row: 0, Col: -1, Rows: 1, Cols: 1},                  // negative col
		{From: 0, To: time.Second, Row: 0, Col: 8, Rows: 1, Cols: 10},                  // cols clamped
		{From: 0, To: time.Second, Row: 3, Col: 0, Rows: 9, Cols: 1},                   // rows clamped
		{From: 0, To: 10 * time.Second, Row: 0, Col: 0, Rows: 1, Cols: 1},              // end clamped
		{From: 6 * time.Second, To: 7 * time.Second, Row: 0, Col: 0, Rows: 1, Cols: 1}, // starts past end
		{From: 0, To: time.Second, Row: 0, Col: 4, Rows: 1, Cols: 0},                   // cols 0 = to right edge
	}
	kept, warnings := Normalize(annotations, 10, 5, 5*time.Second)

	if len(kept) != 5 {
		t.Fatalf("kept %d annotations, want 5: %#v", len(kept), kept)
	}
	if kept[1].Cols != 2 { // col 8 of 10
		t.Errorf("clamped cols = %d, want 2", kept[1].Cols)
	}
	if kept[2].Rows != 2 { // row 3 of 5
		t.Errorf("clamped rows = %d, want 2", kept[2].Rows)
	}
	if kept[3].To != 5*time.Second {
		t.Errorf("clamped end = %v, want 5s", kept[3].To)
	}
	if kept[4].Cols != 6 { // col 4 to right edge of 10
		t.Errorf("right-edge cols = %d, want 6", kept[4].Cols)
	}
	if len(warnings) != 6 {
		t.Errorf("got %d warnings, want 6: %v", len(warnings), warnings)
	}
}

func TestNormalize_EmptyTextIsKept(t *testing.T) {
	kept, warnings := Normalize([]Annotation{
		{From: 0, To: time.Second, Row: 0, Col: 0, Rows: 1, Cols: 2},
	}, 10, 5, 5*time.Second)
	if len(kept) != 1 || len(warnings) != 0 {
		t.Errorf("kept = %v, warnings = %v; a textless highlight is valid", kept, warnings)
	}
}

func TestMapTimeline(t *testing.T) {
	// Events at 1s, 11s, 12s with the 10s gap capped to 2s: 11s -> 3s, 12s -> 4s.
	mapTime := MapTimeline([]float64{1, 11, 12}, []float64{1, 3, 4})

	tests := []struct {
		in, want float64
	}{
		{0, 0},  // before the first event
		{1, 1},  // on an event
		{6, 2},  // halfway into the capped gap
		{11, 3}, // end of the capped gap
		{12, 4}, // after the gap
		{14, 6}, // past the last event keeps the shift
	}
	for _, tt := range tests {
		if got := mapTime(tt.in); got != tt.want {
			t.Errorf("mapTime(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestRemap(t *testing.T) {
	annotations := []Annotation{{From: 2 * time.Second, To: 4 * time.Second, Row: 1, Col: 1, Rows: 1, Cols: 1}}
	remapped := Remap(annotations, func(t float64) float64 { return t / 2 })
	if remapped[0].From != time.Second || remapped[0].To != 2*time.Second {
		t.Errorf("remapped to %v..%v, want 1s..2s", remapped[0].From, remapped[0].To)
	}
	if Remap(nil, nil) != nil {
		t.Error("Remap(nil) should stay nil")
	}
}
