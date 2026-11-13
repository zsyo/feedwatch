//go:build webui

package auth

import (
	"crypto/subtle"
	"net/http"
)

// CheckCSRF compares the session-bound token with the header sent by the SPA.
// Constant-time comparison avoids leaking the token one byte at a time.
func CheckCSRF(expected, supplied string) bool {
	if expected == "" || supplied == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(supplied)) == 1
}

func CSRFHeader(r *http.Request) string { return r.Header.Get(CSRFHeaderName) }

// IsSafeMethod reports whether the request is exempt from CSRF checks.
func IsSafeMethod(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}
