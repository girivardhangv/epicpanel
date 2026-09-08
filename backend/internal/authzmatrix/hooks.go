package authzmatrix

// Hooks set by the api test harness (avoids an import cycle: the matrix
// package must not import api, and api must not import the matrix in
// production code — only its _test files do).

import "net/http"

// RouteInventory is the registered route table (method + pattern set),
// provided by the api package test setup.
var RouteInventory map[string]bool

// ProbeHandler is the fully wired server handler used for role probes.
var ProbeHandler http.Handler
