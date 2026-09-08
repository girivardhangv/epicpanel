package httpapi

import "net/http"

// CSRFGuard defends cookie-authenticated state changes: browsers attach
// cookies to cross-site POSTs, but cannot set our custom header (SameSite=Lax
// remains the first line). Bearer-token auth is exempt (no ambient creds).
const csrfHeader = "X-EpicPanel"

func CSRFGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			// Only cookie sessions are ambients; token auth carries its
			// credential explicitly and is CSRF-immune.
			if !IsAPIToken(r.Context()) && hasSessionCookie(r) {
				if r.Header.Get(csrfHeader) != "1" {
					RespondError(w, ErrForbidden("missing "+csrfHeader+" header"))
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func hasSessionCookie(r *http.Request) bool {
	_, err := r.Cookie("epicpanel_session")
	return err == nil
}
