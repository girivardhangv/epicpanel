package pages

import _ "embed"

//go:embed default.html
var DefaultHTML string

//go:embed suspended.html
var SuspendedHTML string

//go:embed quota_exceeded.html
var QuotaExceededHTML string

//go:embed busy.html
var BusyHTML string

//go:embed notfound.html
var NotFoundHTML string

//go:embed welcome.html
var WelcomeHTML string

// WelcomeFileName is the docroot placeholder written for freshly provisioned
// empty sites ("Website Ready to Be Served"). It is appended to the vhost
// index list, so it only shows when no real index exists; the first deploy
// or customer upload simply shadows (or deletes) it.
const WelcomeFileName = "index.epicpanel-welcome.html"
