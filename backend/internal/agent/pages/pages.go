package pages

import _ "embed"

//go:embed default.html
var DefaultHTML string

//go:embed suspended.html
var SuspendedHTML string

//go:embed quota_exceeded.html
var QuotaExceededHTML string
