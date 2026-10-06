package api

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
)

// RequireToken protects a handler with HTTP Basic auth where the password
// is token (the username is ignored). Basic auth is used so a browser can
// open the dashboard directly: it prompts once and then sends the
// credentials on the page's own /api fetches.
//
// An empty token disables the check; main only allows that when
// ALLOW_INSECURE=true.
func RequireToken(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(token)) != 1 {
			// A browsers first request carries no credentials by design, so
			// only log attempts that presented (wrong) ones.
			if ok {
				slog.Warn("dashboard auth failed", "remote_addr", r.RemoteAddr, "path", r.URL.Path)
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="file-compressor", charset="UTF-8"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
