// Package permissions is the single source of truth for RBAC v2: the
// master-doc role names (Super Admin, Admin, Support, Reseller, Customer)
// mapped onto the existing org_role enum, and granular permission strings
// (server.read, account.suspend, ...) enforced server-side on every route.
//
// Role mapping (no existing grants are dropped):
//
//	Super Admin -> users.is_platform_admin (platform-wide)
//	Admin       -> org role "admin"
//	Support     -> org role "support"
//	Reseller    -> org role "reseller" (new)
//	Customer    -> org role "owner" (the account holder)
//	(staff-ish legacy roles developer/billing are preserved and mapped)
package permissions

import "github.com/epicbyte/epicpanel/backend/internal/organizations"

// Permission strings. The seven from the master doc are verbatim; the rest
// cover the existing route surface so enforcement is deny-by-default.
const (
	ServerRead     = "server.read"
	ServerManage   = "server.manage"
	AccountCreate  = "account.create"
	AccountSuspend = "account.suspend"
	BillingManage  = "billing.manage"
	NodeExecute    = "node.execute"
	BackupRestore  = "backup.restore"

	AccountRead    = "account.read"
	AccountManage  = "account.manage"
	DomainManage   = "domain.manage"
	RuntimeManage  = "runtime.manage"
	DatabaseManage = "database.manage"
	BackupManage   = "backup.manage"
	DeployManage   = "deploy.manage"
	CronManage     = "cron.manage"
	SSHManage      = "ssh.manage"
	TerminalAccess = "terminal.access"
	ServiceManage  = "service.manage"
	UserManage     = "user.manage"
	TokenManage    = "token.manage"
	MemberManage   = "member.manage"
	MonitorRead    = "monitor.read"
	AuditRead      = "audit.read"
	SettingsManage = "settings.manage"
)

// rolePerms maps every org role to its permission set. Platform admins
// (Super Admin) hold everything and bypass this map.
var rolePerms = map[organizations.Role]map[string]bool{
	organizations.RoleOwner: subset(
		ServerRead, AccountRead, AccountManage, AccountCreate, DomainManage,
		RuntimeManage, DatabaseManage, BackupManage, BackupRestore, DeployManage,
		CronManage, SSHManage, TerminalAccess, ServiceManage, MonitorRead,
		AuditRead, TokenManage, MemberManage,
	),
	organizations.RoleAdmin: subset(
		ServerRead, ServerManage, AccountRead, AccountManage, AccountCreate, AccountSuspend,
		DomainManage, RuntimeManage, DatabaseManage, BackupManage, BackupRestore, DeployManage,
		CronManage, SSHManage, TerminalAccess, ServiceManage, MonitorRead, AuditRead,
		TokenManage, MemberManage,
	),
	// Reseller: manages its customers' accounts/services but not the fleet.
	organizations.RoleReseller: subset(
		ServerRead, AccountRead, AccountManage, AccountCreate, AccountSuspend,
		DomainManage, DatabaseManage, BackupManage, DeployManage, MonitorRead,
		ServiceManage, MemberManage,
	),
	organizations.RoleDeveloper: subset(
		ServerRead, AccountRead, AccountManage, AccountCreate, DomainManage,
		RuntimeManage, DatabaseManage, BackupManage, DeployManage, CronManage,
		SSHManage, TerminalAccess, ServiceManage, MonitorRead,
	),
	organizations.RoleBilling: subset(
		AccountRead, BillingManage, ServiceManage, MonitorRead, AuditRead,
	),
	organizations.RoleSupport: subset(
		ServerRead, AccountRead, MonitorRead, TerminalAccess,
	),
}

func subset(perms ...string) map[string]bool {
	m := make(map[string]bool, len(perms))
	for _, p := range perms {
		m[p] = true
	}
	return m
}

// Can reports whether an org role grants a permission.
func Can(role organizations.Role, perm string) bool {
	return rolePerms[role][perm]
}

// All returns the full permission set for a role (nil for unknown roles).
func All(role organizations.Role) map[string]bool {
	return rolePerms[role]
}

// tokenScopeMap translates permission strings into the api-token scope
// vocabulary (group:read / group:write). Absence = tokens cannot hold the
// permission at all (deny-by-default).
var tokenScopeMap = map[string]string{
	ServerRead:     "servers:read",
	ServerManage:   "servers:write",
	AccountRead:    "websites:read",
	AccountCreate:  "websites:write",
	AccountManage:  "websites:write",
	DomainManage:   "domains:write",
	RuntimeManage:  "runtimes:write",
	DatabaseManage: "databases:write",
	BackupManage:   "backups:write",
	BackupRestore:  "backups:write",
	DeployManage:   "deployments:write",
	CronManage:     "websites:write",
	SSHManage:      "websites:write",
	MonitorRead:    "monitoring:read",
	MemberManage:   "org:write",
	TokenManage:    "org:write",
	AuditRead:      "audit:read",
}

// TokenScopeFor maps a permission string to the api-token scope that covers
// it (second return false = tokens can never hold this permission).
func TokenScopeFor(perm string) (string, bool) {
	s, ok := tokenScopeMap[perm]
	return s, ok
}
