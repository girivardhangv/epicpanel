package httpapi

import (
	"net/http"
	"strings"
)

type Router struct {
	mux *http.ServeMux
}

func NewRouter() *Router { return &Router{mux: http.NewServeMux()} }

func (rt *Router) Handle(method, pattern string, h http.HandlerFunc) {
	rt.mux.HandleFunc(method+" "+pattern, h)
}

func (rt *Router) Handler() http.Handler { return rt.mux }

func PathID(r *http.Request) string {
	p := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(p, "/")
	return parts[len(parts)-1]
}
