package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"file-compressor/internal/store"
)

func TestRetryJobRequiresCustomHeader(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mux := http.NewServeMux()
	mux.Handle("POST /api/jobs/{id}/retry", RetryJob(db))

	do := func(header bool, id string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/jobs/"+id+"/retry", nil)
		if header {
			req.Header.Set("X-Requested-With", "dashboard")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}

	if c := do(false, "x"); c != http.StatusForbidden {
		t.Errorf("no header: %d, want 403", c)
	}
	if c := do(true, "missing"); c != http.StatusNotFound {
		t.Errorf("unknown id: %d, want 404", c)
	}

	db.Enqueue("b", "o")
	j, _ := db.ClaimNext()
	db.Fail(j.ID, http.ErrAbortHandler, 1, true)
	if c := do(true, j.ID); c != http.StatusNoContent {
		t.Errorf("dead-letter job: %d, want 204", c)
	}
}
