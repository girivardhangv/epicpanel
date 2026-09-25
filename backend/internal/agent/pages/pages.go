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

//go:embed terminated.html
var TerminatedHTML string

// BandwidthExhaustedHTML is a TEMPLATE, not a static page: {{USED}},
// {{LIMIT}}, {{RESETS}} and {{PERCENT}} are filled per site at suspend
// time and written to /srv/epicpanel/websites/<id>/pages/ — never into the
// shared default_pages dir (site A's numbers must not leak to site B).
//go:embed bandwidth_exhausted.html
var BandwidthExhaustedHTML string

//go:embed welcome.html
var WelcomeHTML string

// WelcomeFileName is the docroot placeholder written for freshly provisioned
// empty sites ("Website Ready to Be Served"). It is appended to the vhost
// index list, so it only shows when no real index exists; the first deploy
// or customer upload simply shadows (or deletes) it.
const WelcomeFileName = "index.epicpanel-welcome.html"
