package queue

import (
	"strings"
	"testing"

	"file-compressor/internal/config"
)

func event(name, bucket, key string) string {
	return `{"Records":[{"eventName":"` + name + `","s3":{"bucket":{"name":"` + bucket + `"},"object":{"key":"` + key + `"}}}]}`
}

func parse(t *testing.T, body string) []Target {
	t.Helper()
	got, err := ParseEvent(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func reset() {
	config.OutputBucket = ""
	config.AllowedBuckets = nil
	config.DLQBucket = "dead"
}

func TestParseEventDecodesKeys(t *testing.T) {
	reset()
	cases := map[string]string{
		"my+photo.jpg":      "my photo.jpg",
		"my%20photo.jpg":    "my photo.jpg",
		"a%2Bb.jpg":         "a+b.jpg",
		"dir/%C3%BCber.png": "dir/über.png",
		"plain/name.jpg":    "plain/name.jpg",
	}
	for raw, want := range cases {
		got := parse(t, event("s3:ObjectCreated:Put", "b", raw))
		if len(got) != 1 || got[0].Object != want {
			t.Errorf("key %q: got %+v, want object %q", raw, got, want)
		}
	}
}

func TestParseEventFilters(t *testing.T) {
	reset()

	if got := parse(t, event("s3:ObjectRemoved:Delete", "b", "a.jpg")); len(got) != 0 {
		t.Error("delete event was queued")
	}
	if got := parse(t, event("s3:ObjectCreated:Put", "dead", "a.jpg")); len(got) != 0 {
		t.Error("DLQ bucket event was queued")
	}
	if got := parse(t, event("s3:ObjectCreated:Put", "b", "a.txt.zst")); len(got) != 0 {
		t.Error(".zst sibling was queued")
	}

	config.AllowedBuckets = []string{"photos"}
	if got := parse(t, event("s3:ObjectCreated:Put", "other", "a.jpg")); len(got) != 0 {
		t.Error("bucket outside ALLOWED_BUCKETS was queued")
	}
	if got := parse(t, event("s3:ObjectCreated:CompleteMultipartUpload", "photos", "a.jpg")); len(got) != 1 {
		t.Error("allowed bucket / multipart create was dropped")
	}
}

func TestParseEventBadJSON(t *testing.T) {
	if _, err := ParseEvent(strings.NewReader("{")); err == nil {
		t.Error("expected error")
	}
}

func TestParseEventIgnoresOutputBucket(t *testing.T) {
	reset()
	config.OutputBucket = "compressed"
	defer reset()

	if got := parse(t, event("s3:ObjectCreated:Put", "compressed", "a.jpg")); len(got) != 0 {
		t.Error("output bucket event was queued; results would be reprocessed forever")
	}
	if got := parse(t, event("s3:ObjectCreated:Put", "uploads", "a.jpg")); len(got) != 1 {
		t.Error("source bucket event was dropped")
	}
}
