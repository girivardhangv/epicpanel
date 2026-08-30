package platform

// Exported constructors for the ops layer. The concrete implementations are
// chosen at compile time per GOOS (webserver_linux.go / webserver_windows.go
// and php_linux.go / php_windows.go).

// NewWebServer returns the web-server control surface; a nil-safe instance is
// returned when nginx is not installed so Status can report honestly.
func NewWebServer() (WebServerOps, error) { return newWebServer() }

// NewPHPRuntime returns the PHP runtime surface for this platform.
func NewPHPRuntime() (PHPOps, error) { return newPHPRuntime() }

// NewFSOps returns the constrained filesystem surface rooted at sitesRoot.
func NewFSOps(sitesRoot string) (FSOps, error) { return newFSOps(sitesRoot) }
