// Package discord implements Discord bot hosting as a first-class workload
// (Phase 8). Bots are their own entity (bot_instances), their own state
// machine, their own file layout (/srv/epicpanel/bots/<bot_id>) and their
// own systemd unit namespace — never modeled as a website.
//
// Runtime abstraction: BotRuntime describes how a bot runs for one language
// runtime. Adding a runtime = adding a provider in this file only; core
// service code contains zero runtime-specific branches.
package discord

import (
	"fmt"
	"sort"
	"strings"
)

// BotRuntime is the runtime abstraction. Implementations: NodeRuntime,
// PythonRuntime (fully supported) and JavaRuntime (foundation stub, listed
// in the master-doc architecture diagram).
type BotRuntime interface {
	// Name is the wire identifier ("node" | "python" | "java").
	Name() string
	// Supported reports whether the control plane will schedule installs for
	// this runtime. Java is a foundation stub: the interface slot exists,
	// scheduling returns a clear error until a provider lands.
	Supported() bool
	// Versions are the selectable runtime versions (offer + default first).
	Versions() []string
	// DefaultVersion is used when the customer does not pick one.
	DefaultVersion() string
	// ValidateVersion checks a customer-selected version against the offer.
	ValidateVersion(v string) error
	// ValidateStartup checks the startup file/command shape for this runtime.
	ValidateStartup(startupFile, startupCommand string) error
	// DefaultStartup returns the startup file used when none is given.
	DefaultStartup() string
	// BuildCommand is the dependency-install command run in the bot root
	// (timeout-bounded by the agent). Empty = no dependency step.
	BuildCommand(botRoot string) string
	// StartCommand renders the process command line for the systemd unit.
	StartCommand(botRoot, startupFile string, port int) string
	// RuntimeBin is the interpreter binary name the agent looks for.
	RuntimeBin() string
}

// NodeRuntime implements BotRuntime for Node.js bots.
type NodeRuntime struct{}

func (NodeRuntime) Name() string       { return "node" }
func (NodeRuntime) Supported() bool    { return true }
func (NodeRuntime) Versions() []string { return []string{"22", "20", "18"} }
func (NodeRuntime) DefaultVersion() string { return "22" }

func (NodeRuntime) ValidateVersion(v string) error {
	for _, avail := range (NodeRuntime{}).Versions() {
		if avail == v {
			return nil
		}
	}
	return fmt.Errorf("unsupported Node.js version %q (available: 22, 20, 18)", v)
}

func (NodeRuntime) ValidateStartup(startupFile, startupCommand string) error {
	if strings.TrimSpace(startupFile) == "" && strings.TrimSpace(startupCommand) == "" {
		return fmt.Errorf("startup_file or startup_command is required for Node.js bots")
	}
	return nil
}

func (NodeRuntime) DefaultStartup() string { return "index.js" }

func (NodeRuntime) BuildCommand(botRoot string) string {
	// npm ci when the lockfile exists (agent decides); npm install otherwise.
	return "npm"
}

func (NodeRuntime) StartCommand(botRoot, startupFile string, port int) string {
	if startupFile == "" {
		startupFile = "index.js"
	}
	return "node " + startupFile
}

func (NodeRuntime) RuntimeBin() string { return "node" }

// PythonRuntime implements BotRuntime for Python bots.
type PythonRuntime struct{}

func (PythonRuntime) Name() string       { return "python" }
func (PythonRuntime) Supported() bool    { return true }
func (PythonRuntime) Versions() []string { return []string{"3.12", "3.11", "3.10"} }
func (PythonRuntime) DefaultVersion() string { return "3.12" }

func (PythonRuntime) ValidateVersion(v string) error {
	for _, avail := range (PythonRuntime{}).Versions() {
		if avail == v {
			return nil
		}
	}
	return fmt.Errorf("unsupported Python version %q (available: 3.12, 3.11, 3.10)", v)
}

func (PythonRuntime) ValidateStartup(startupFile, startupCommand string) error {
	if strings.TrimSpace(startupFile) == "" && strings.TrimSpace(startupCommand) == "" {
		return fmt.Errorf("startup_file or startup_command is required for Python bots")
	}
	return nil
}

func (PythonRuntime) DefaultStartup() string { return "bot.py" }

func (PythonRuntime) BuildCommand(botRoot string) string {
	// venv + pip install -r requirements.txt (agent assembles the venv path).
	return "pip"
}

func (PythonRuntime) StartCommand(botRoot, startupFile string, port int) string {
	if startupFile == "" {
		startupFile = "bot.py"
	}
	return "python " + startupFile
}

func (PythonRuntime) RuntimeBin() string { return "python" }

// JavaRuntime is the foundation stub: the runtime slot exists so adding Java
// later is a new provider file only, but scheduling is refused honestly.
type JavaRuntime struct{}

func (JavaRuntime) Name() string       { return "java" }
func (JavaRuntime) Supported() bool    { return false }
func (JavaRuntime) Versions() []string { return nil }
func (JavaRuntime) DefaultVersion() string { return "" }

func (JavaRuntime) ValidateVersion(v string) error {
	return fmt.Errorf("java runtime is not yet available for bots (foundation only)")
}

func (JavaRuntime) ValidateStartup(startupFile, startupCommand string) error {
	return fmt.Errorf("java runtime is not yet available for bots (foundation only)")
}

func (JavaRuntime) DefaultStartup() string { return "" }
func (JavaRuntime) BuildCommand(botRoot string) string { return "" }
func (JavaRuntime) StartCommand(botRoot, startupFile string, port int) string { return "" }
func (JavaRuntime) RuntimeBin() string { return "java" }

// runtimes is the provider registry. Adding a runtime = one entry here.
var runtimes = map[string]BotRuntime{
	NodeRuntime{}.Name():   NodeRuntime{},
	PythonRuntime{}.Name(): PythonRuntime{},
	JavaRuntime{}.Name():   JavaRuntime{},
}

// RuntimeFor resolves the provider for a runtime name.
func RuntimeFor(name string) (BotRuntime, error) {
	rt, ok := runtimes[name]
	if !ok {
		return nil, fmt.Errorf("unknown bot runtime %q", name)
	}
	return rt, nil
}

// RuntimeNames lists available runtimes (sorted, for UIs and validation).
func RuntimeNames() []string {
	out := make([]string, 0, len(runtimes))
	for name := range runtimes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// VersionOffer is the runtime + versions payload for the UI version selector.
type VersionOffer struct {
	Runtime string   `json:"runtime"`
	Label   string   `json:"label"`
	Versions []string `json:"versions"`
	Default string   `json:"default"`
}

// VersionOffers returns every runtime's selectable versions (Java included,
// flagged by empty Versions so the UI can show it as coming soon).
func VersionOffers() []VersionOffer {
	out := []VersionOffer{
		{Runtime: "node", Label: "Node.js", Versions: NodeRuntime{}.Versions(), Default: NodeRuntime{}.DefaultVersion()},
		{Runtime: "python", Label: "Python", Versions: PythonRuntime{}.Versions(), Default: PythonRuntime{}.DefaultVersion()},
		{Runtime: "java", Label: "Java (coming soon)", Versions: nil, Default: ""},
	}
	return out
}
