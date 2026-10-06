package processor

import (
	"testing"

	"file-compressor/internal/config"
)

func TestDestination(t *testing.T) {
	defer func() { config.OutputBucket = "" }()

	inPlace := &Output{Key: "a.jpg"}
	sibling := &Output{Key: "a.txt.zst"}

	config.OutputBucket = ""
	if d := Destination("src", "a.jpg", inPlace); d != (Dest{Bucket: "src", IfMatch: true, Replaces: true}) {
		t.Errorf("in-place image: %+v", d)
	}
	if d := Destination("src", "a.txt", sibling); d != (Dest{Bucket: "src"}) {
		t.Errorf("sibling: %+v", d)
	}

	// Non-destructive mode: never overwrite the source, so no ETag guard.
	config.OutputBucket = "out"
	for _, o := range []*Output{inPlace, sibling} {
		if d := Destination("src", "a.jpg", o); d != (Dest{Bucket: "out", Replaces: true}) {
			t.Errorf("copy mode %q: %+v", o.Key, d)
		}
	}
}
