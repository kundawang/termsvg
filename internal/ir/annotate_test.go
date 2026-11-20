package ir

import (
	"testing"
	"time"

	"github.com/mrmarble/termsvg/internal/annotate"
	"github.com/mrmarble/termsvg/internal/asciicast"
)

// processWithAnnotations processes a cast of simple output events with the
// given annotation track and returns the mapped track.
func processWithAnnotations(t *testing.T, cast *asciicast.Cast, config *ProcessorConfig) *Recording {
	t.Helper()
	rec, err := NewProcessor(config).Process(cast)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestProcess_AnnotationsFollowIdleCap(t *testing.T) {
	// A 10s idle gap between 1s and 11s is capped to 2s.
	cast := &asciicast.Cast{
		Header: asciicast.Header{Width: 10, Height: 3},
		Events: []asciicast.Event{
			{Time: 1, EventType: asciicast.Output, EventData: "a"},
			{Time: 11, EventType: asciicast.Output, EventData: "b"},
			{Time: 12, EventType: asciicast.Output, EventData: "c"},
		},
	}
	config := DefaultProcessorConfig()
	config.IdleTimeLimit = 2 * time.Second
	config.Annotations = []annotate.Annotation{
		{From: 6 * time.Second, To: 11500 * time.Millisecond, Row: 0, Col: 0, Rows: 1, Cols: 1, Text: "inside the pause"},
	}

	rec := processWithAnnotations(t, cast, config)

	if rec.Duration != 4*time.Second {
		t.Fatalf("duration = %v, want 4s after the idle cap", rec.Duration)
	}
	ann := rec.Annotations[0]
	// Halfway into the 10s gap maps to halfway into the capped 2s gap.
	if ann.From != 2*time.Second {
		t.Errorf("From = %v, want 2s (halfway into the capped gap)", ann.From)
	}
	if ann.To != 3500*time.Millisecond {
		t.Errorf("To = %v, want 3.5s", ann.To)
	}
}

func TestProcess_AnnotationsFollowSpeed(t *testing.T) {
	cast := &asciicast.Cast{
		Header: asciicast.Header{Width: 10, Height: 3},
		Events: []asciicast.Event{
			{Time: 1, EventType: asciicast.Output, EventData: "a"},
			{Time: 2, EventType: asciicast.Output, EventData: "b"},
		},
	}
	config := DefaultProcessorConfig()
	config.Speed = 2
	config.Annotations = []annotate.Annotation{
		{From: time.Second, To: 2 * time.Second, Row: 0, Col: 0, Rows: 1, Cols: 1},
	}

	rec := processWithAnnotations(t, cast, config)

	ann := rec.Annotations[0]
	if ann.From != 500*time.Millisecond || ann.To != time.Second {
		t.Errorf("interval = %v..%v, want 0.5s..1s at 2x speed", ann.From, ann.To)
	}
}

func TestProcess_NoAnnotationsKeepsRecordingClean(t *testing.T) {
	cast := &asciicast.Cast{
		Header: asciicast.Header{Width: 10, Height: 3},
		Events: []asciicast.Event{
			{Time: 0.1, EventType: asciicast.Output, EventData: "a"},
		},
	}
	rec := processWithAnnotations(t, cast, DefaultProcessorConfig())
	if rec.Annotations != nil {
		t.Errorf("Annotations = %v, want nil without a track", rec.Annotations)
	}
}
