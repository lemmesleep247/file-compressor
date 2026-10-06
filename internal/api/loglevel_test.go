package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"file-compressor/internal/logging"
)

func TestLogLevelEndpoint(t *testing.T) {
	defer logging.SetLevel("info")
	h := LogLevel()

	do := func(method, url string, xhr bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, url, nil)
		if xhr {
			req.Header.Set("X-Requested-With", "dashboard")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(http.MethodPost, "/api/loglevel?level=debug", false); rec.Code != http.StatusForbidden {
		t.Errorf("POST without header: %d", rec.Code)
	}
	if rec := do(http.MethodPost, "/api/loglevel?level=loud", true); rec.Code != http.StatusBadRequest {
		t.Errorf("bad level: %d", rec.Code)
	}
	if rec := do(http.MethodPost, "/api/loglevel?level=debug", true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"debug"`) {
		t.Errorf("set debug: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodGet, "/api/loglevel", false); !strings.Contains(rec.Body.String(), `"debug"`) {
		t.Errorf("GET after set: %s", rec.Body.String())
	}
}
