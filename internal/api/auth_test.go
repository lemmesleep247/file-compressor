package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireToken(t *testing.T) {
	h := RequireToken("s3cret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	do := func(setAuth func(*http.Request)) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		setAuth(req)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if c := do(func(*http.Request) {}); c != http.StatusUnauthorized {
		t.Errorf("no credentials: %d", c)
	}
	if c := do(func(r *http.Request) { r.SetBasicAuth("x", "wrong") }); c != http.StatusUnauthorized {
		t.Errorf("wrong token: %d", c)
	}
	if c := do(func(r *http.Request) { r.SetBasicAuth("anyone", "s3cret") }); c != http.StatusOK {
		t.Errorf("right token: %d", c)
	}
}
