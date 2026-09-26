package events

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOriginAllowed(t *testing.T) {
	req := func(origin string, https bool) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://panel.example:8080/v1/ws", nil)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if https {
			r.Header.Set("X-Forwarded-Proto", "https")
		}
		return r
	}
	devDefaults := []string{"http://localhost:5173", "http://127.0.0.1:5173"}

	cases := []struct {
		name string
		req  *http.Request
		want bool
	}{
		{"no origin (curl/servers) allowed", req("", false), true},
		{"same-origin http allowed", req("http://panel.example:8080", false), true},
		{"same-origin behind https proxy allowed", req("https://panel.example:8080", true), true},
		{"configured dev origin allowed", req("http://localhost:5173", false), true},
		{"foreign origin rejected", req("http://evil.example", false), false},
		{"same host different scheme rejected", req("https://panel.example:8080", false), false},
	}
	for _, tc := range cases {
		if got := originAllowed(tc.req, devDefaults); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
