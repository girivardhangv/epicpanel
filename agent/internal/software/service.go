package software

// ServiceMode describes how a component's service is controlled on the host.
type ServiceMode string

const (
	// ServiceNone means the component runs on demand (no persistent service).
	ServiceNone ServiceMode = "none"
	// ServiceSystemd means the component is managed by a systemd unit.
	ServiceSystemd ServiceMode = "systemd"
	// ServiceProcess means the agent supervises the binary directly (Windows).
	ServiceProcess ServiceMode = "process"
	// ServiceWindowsService means a real Windows service registered via sc.
	ServiceWindowsService ServiceMode = "windows-service"
)

// serviceSpec carries everything needed to start/stop a component as a
// service. It is built from static provider data, never from request input.
type serviceSpec struct {
	mode   ServiceMode
	unit   string   // systemd unit name (e.g. "nginx")
	bin    string   // binary to supervise (Windows process mode)
	args   []string // launch args (Windows process mode)
	pidDir string   // where the agent writes its own pid file (Windows)
}
