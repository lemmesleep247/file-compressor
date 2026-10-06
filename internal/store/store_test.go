package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestEnqueueDedupesQueuedOnly(t *testing.T) {
	s := open(t)

	if _, created, _ := s.Enqueue("b", "o"); !created {
		t.Fatal("first enqueue not created")
	}
	if _, created, _ := s.Enqueue("b", "o"); created {
		t.Fatal("duplicate queued job was created")
	}

	// While the first is processing, a new event (maybe a newer upload)
	// must still be accepted...
	if j, _ := s.ClaimNext(); j == nil {
		t.Fatal("nothing to claim")
	}
	if _, created, _ := s.Enqueue("b", "o"); !created {
		t.Fatal("event during processing was dropped")
	}

	// ...but must not run concurrently with the in-flight job.
	if j, _ := s.ClaimNext(); j != nil {
		t.Fatal("claimed a second job for an object already processing")
	}
}

func TestClaimAfterCompletion(t *testing.T) {
	s := open(t)
	s.Enqueue("b", "o")
	first, _ := s.ClaimNext()
	s.Enqueue("b", "o")

	if err := s.Complete(first.ID, 10, 5); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.ClaimNext(); j == nil {
		t.Fatal("follow-up job not claimable after the first completed")
	}
}

func TestFailRetriesThenDeadLetters(t *testing.T) {
	s := open(t)
	s.Enqueue("b", "o")

	j, _ := s.ClaimNext()
	dead, err := s.Fail(j.ID, errors.New("boom"), 2, false)
	if err != nil || dead {
		t.Fatalf("first failure: dead=%v err=%v", dead, err)
	}

	// Backoff means it isn't immediately claimable.
	if next, _ := s.ClaimNext(); next != nil {
		t.Fatal("retry claimed before backoff elapsed")
	}

	dead, err = s.Fail(j.ID, errors.New("boom"), 2, false)
	if err != nil || !dead {
		t.Fatalf("second failure: dead=%v err=%v", dead, err)
	}
	st, _ := s.Stats()
	if st.DeadLetter != 1 {
		t.Fatalf("dead_letter = %d", st.DeadLetter)
	}
}

func TestRequeueStuckProcessing(t *testing.T) {
	s := open(t)
	s.Enqueue("b", "o")
	s.ClaimNext()

	if err := s.RequeueStuckProcessing(); err != nil {
		t.Fatal(err)
	}
	if j, _ := s.ClaimNext(); j == nil {
		t.Fatal("stuck job was not requeued")
	}
}

func TestFailPermanentSkipsRetries(t *testing.T) {
	s := open(t)
	s.Enqueue("b", "o")

	j, _ := s.ClaimNext()
	dead, err := s.Fail(j.ID, errors.New("corrupt"), 5, true)
	if err != nil || !dead {
		t.Fatalf("dead=%v err=%v", dead, err)
	}
}

func TestRetryOnlyRevivesDeadLetterJobs(t *testing.T) {
	s := open(t)
	s.Enqueue("b", "o")
	j, _ := s.ClaimNext()

	if ok, _ := s.Retry(j.ID); ok {
		t.Fatal("retried a job that is still processing")
	}
	if ok, _ := s.Retry("nope"); ok {
		t.Fatal("retried an unknown id")
	}

	s.Fail(j.ID, errors.New("boom"), 1, true)
	ok, err := s.Retry(j.ID)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}

	again, _ := s.ClaimNext()
	if again == nil || again.ID != j.ID || again.Retries != 0 {
		t.Fatalf("revived job not claimable with fresh budget: %+v", again)
	}
	if ok, _ := s.Retry(j.ID); ok {
		t.Fatal("double retry succeeded")
	}
}
