package store

import (
	"testing"
	"time"
)

func TestPruneRemovesOnlyOldFinishedJobs(t *testing.T) {
	s := open(t)

	s.Enqueue("b", "done")
	s.Enqueue("b", "queued")
	j, _ := s.ClaimNext() // "done" (oldest)
	s.Complete(j.ID, 10, 5)

	// Everything is "old" relative to a future cutoff, but only the
	// finished job may go.
	n, err := s.Prune(time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, err=%v; want 1", n, err)
	}

	st, _ := s.Stats()
	if st.Queued != 1 || st.Completed != 0 {
		t.Fatalf("stats after prune: %+v", st)
	}

	// A cutoff in the past removes nothing.
	s.Enqueue("b", "x")
	j2, _ := s.ClaimNext()
	s.Complete(j2.ID, 1, 1)
	if n, _ := s.Prune(time.Now().Add(-time.Hour)); n != 0 {
		t.Fatalf("pruned %d recent jobs", n)
	}
}
