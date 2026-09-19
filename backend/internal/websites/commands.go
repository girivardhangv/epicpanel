package websites

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

// ============================================================================
// SITE COMMANDS (user-facing composer / npm / artisan / node access)
// ============================================================================

// siteCommandBinaries is the allowlist of executables a user may run inside
// their own site tree. The agent re-validates this list (defense in depth)
// and execs argv directly — no shell, no quoting, nothing else.
var siteCommandBinaries = map[string]bool{
	"composer": true, "php": true, "artisan": true, "wp": true,
	"node": true, "npm": true, "npx": true, "yarn": true, "pnpm": true,
	"python3": true, "pip3": true, "pip": true, "python": true,
	"git": true, "go": true, "grep": true, "cat": true, "ls": true,
}

const (
	siteCommandMaxTokens = 24
	siteCommandMaxLen    = 512
)

// ValidateSiteCommand splits a raw command string into argv and enforces the
// allowlist + token charset (no shell metacharacters, no whitespace tricks —
// the agent execs these tokens verbatim under the site user).
func ValidateSiteCommand(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errStr("command is required")
	}
	if len(raw) > siteCommandMaxLen {
		return nil, errStr("command too long (max 512 chars)")
	}
	if strings.ContainsAny(raw, "\n\r\t") {
		return nil, errStr("multi-line commands are not permitted")
	}
	argv := strings.Fields(raw)
	if len(argv) > siteCommandMaxTokens {
		return nil, errStr("command has too many tokens (max 24)")
	}
	if !siteCommandBinaries[argv[0]] {
		return nil, errStr("command \"" + argv[0] + "\" is not allowed — permitted: composer, php, artisan, wp, node, npm, npx, yarn, pnpm, python3, pip3, git, go")
	}
	for _, tok := range argv {
		if len(tok) > 256 {
			return nil, errStr("token too long")
		}
		for _, c := range tok {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			case c == '-' || c == '_' || c == '.' || c == '/' || c == '@' ||
				c == ':' || c == '=' || c == '+' || c == '~' || c == '%':
			default:
				return nil, errStr("invalid character in command (shell metacharacters are not permitted)")
			}
		}
	}
	return argv, nil
}

type SiteCommandRequest struct {
	Command string `json:"command"`
}

// RunSiteCommand queues an allowlisted command to run inside the site tree
// as the site's Unix user. Output arrives in the job result — the frontend
// polls GET /websites/{id}/jobs for it.
func (h *Handler) RunSiteCommand(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if ws.Status != StatusReady {
		httpapi.RespondError(w, httpapi.ErrConflict("website must be ready before running commands"))
		return
	}
	if ws.Runtime == RuntimeStatic || ws.WebServer == "none" {
		httpapi.RespondError(w, httpapi.ErrValidation("static sites have no runtime to run commands in"))
		return
	}

	var req SiteCommandRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	argv, err := ValidateSiteCommand(req.Command)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}

	job, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeSiteCommand, map[string]any{
		"website_id":      ws.ID.String(),
		"unix_user":       ws.UnixUser,
		"runtime":         string(ws.Runtime),
		"runtime_version": ws.RuntimeVersion,
		"document_root":   ws.DocumentRoot,
		"argv":            argv,
	})
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	if h.Audit != nil {
		usr, _ := httpapi.UserFrom(r.Context())
		var actorID *uuid.UUID
		if usr != nil {
			if uid, err := uuid.Parse(usr.ID); err == nil {
				actorID = &uid
			}
		}
		h.Audit.RecordBestEffort(r.Context(), audit.Entry{
			OrganizationID: &orgID,
			ActorUserID:    actorID,
			ActorType:      audit.ActorUser,
			Action:         "website.command",
			ResourceType:   "website",
			ResourceID:     ws.ID.String(),
			Metadata:       map[string]any{"argv": argv},
		})
	}

	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{
		"job_id": job.ID,
		"argv":   argv,
	})
}
