// Console command allowlist (Phase 7): customers drive the server console
// ONLY through this list — never a shell, never arbitrary input. The list
// mirrors safe Minecraft server commands; anything else is rejected at the
// API AND re-validated agent-side before the RCON send.
package minecraft

import (
	"fmt"
	"strings"
	"time"
)

// CommandSpec is one allowlisted console command shape.
type CommandSpec struct {
	// Name is the leading word ("list", "say", ...).
	Name string
	// MaxArgs bounds the argument count (0 = no args allowed).
	MaxArgs int
	// Description for UIs.
	Description string
}

// ConsoleAllowlist is the controlled command surface. Adding a command =
// one entry here. Everything not on this list is refused — no exceptions.
var ConsoleAllowlist = []CommandSpec{
	{Name: "list", MaxArgs: 0, Description: "List online players"},
	{Name: "say", MaxArgs: 12, Description: "Broadcast a message"},
	{Name: "whitelist", MaxArgs: 2, Description: "Whitelist add/remove/list"},
	{Name: "kick", MaxArgs: 2, Description: "Kick a player"},
	{Name: "ban", MaxArgs: 2, Description: "Ban a player"},
	{Name: "pardon", MaxArgs: 1, Description: "Unban a player"},
	{Name: "op", MaxArgs: 1, Description: "Grant operator to a player"},
	{Name: "deop", MaxArgs: 1, Description: "Revoke operator from a player"},
	{Name: "save-all", MaxArgs: 1, Description: "Save the world (flush)"},
	{Name: "save-on", MaxArgs: 0, Description: "Enable autosave"},
	{Name: "save-off", MaxArgs: 0, Description: "Disable autosave (for snapshots)"},
	{Name: "tps", MaxArgs: 0, Description: "Show TPS (Paper/Purpur)"},
	{Name: "difficulty", MaxArgs: 1, Description: "Set difficulty"},
	{Name: "weather", MaxArgs: 2, Description: "Set weather"},
	{Name: "time", MaxArgs: 2, Description: "Set time"},
	{Name: "gamemode", MaxArgs: 2, Description: "Set a player's gamemode"},
	{Name: "stop", MaxArgs: 0, Description: "Graceful server stop"},
}

// allowIndex is the lookup map.
var allowIndex = func() map[string]CommandSpec {
	m := make(map[string]CommandSpec, len(ConsoleAllowlist))
	for _, c := range ConsoleAllowlist {
		m[c.Name] = c
	}
	return m
}()

// ValidateConsoleCommand enforces the allowlist + argument shape. The
// returned command is the trimmed, single-spaced normalized string that is
// safe to hand to RCON. Rejection is total: no partial execution.
func ValidateConsoleCommand(raw string) (string, error) {
	// Control characters in the raw input are refused outright (no silent
	// normalization that could mask an injection attempt).
	for _, c := range raw {
		if c == '\n' || c == '\r' || c == '\x00' || (c < 0x20 && c != '\t') {
			return "", fmt.Errorf("command contains control characters")
		}
	}
	cmd := strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if cmd == "" {
		return "", fmt.Errorf("command is empty")
	}
	if len(cmd) > 512 {
		return "", fmt.Errorf("command too long (max 512 chars)")
	}
	parts := strings.Split(cmd, " ")
	name := parts[0]
	// Commands are case-insensitive on the server; normalize to lowercase
	// for matching and send the normalized form.
	name = strings.ToLower(name)
	spec, ok := allowIndex[name]
	if !ok {
		return "", fmt.Errorf("command %q is not on the console allowlist", name)
	}
	args := parts[1:]
	if len(args) > spec.MaxArgs {
		return "", fmt.Errorf("%s accepts at most %d argument(s)", name, spec.MaxArgs)
	}
	for _, a := range args {
		if err := validateArg(a); err != nil {
			return "", err
		}
	}
	out := append([]string{name}, args...)
	return strings.Join(out, " "), nil
}

// validateArg bounds one command argument: no shell metacharacters, no
// control characters, bounded length (this is a Minecraft console argument,
// not a shell fragment).
func validateArg(a string) error {
	if len(a) > 256 {
		return fmt.Errorf("command argument too long (max 256 chars)")
	}
	for _, c := range a {
		switch c {
		case ';', '|', '&', '$', '`', '<', '>', '(', ')', '{', '}', '\\', '"', '\'', '*', '?', '[', ']', '#', '~', '!':
			return fmt.Errorf("command arguments cannot contain %q", string(c))
		}
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("command arguments cannot contain control characters")
		}
	}
	return nil
}

// IsAllowlisted reports membership (agent-side double check).
func IsAllowlisted(name string) bool {
	_, ok := allowIndex[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// ParseCron validates a 5-field cron expression and returns the interval to
// the next fire time from now (same semantics as the bot scheduler:
// minute hour day-of-month month day-of-week, *, lists, ranges, */n steps).
func ParseCron(expr string) (dur time.Duration, err error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return 0, fmt.Errorf("cron must have 5 fields (minute hour day-of-month month day-of-week)")
	}
	now := time.Now().UTC().Truncate(time.Minute)
	next, err := nextCronFire(fields, now.Add(time.Minute))
	if err != nil {
		return 0, err
	}
	return next.Sub(now), nil
}

// NextCronAfter returns the interval to the next fire after at.
func NextCronAfter(expr string, at time.Time) time.Duration {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return 0
	}
	next, err := nextCronFire(fields, at.Truncate(time.Minute).Add(time.Minute))
	if err != nil {
		return 0
	}
	return next.Sub(at)
}
