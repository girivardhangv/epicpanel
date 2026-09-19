package websites

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

// nginxDirectiveRe is the allowlist of nginx directives accepted in the
// per-site rewrite/config snippet. Everything else is rejected server-side
// AND agent-side, so a stored snippet can never inject arbitrary nginx
// configuration (e.g. `include`, `root`, `alias`, `daemon`).
var nginxDirectiveRe = regexp.MustCompile(`^[a-z_]+$`)

var nginxAllowedDirectives = map[string]bool{
	"rewrite": true, "if": true, "return": true, "set": true, "break": true,
	"expires": true, "add_header": true, "try_files": true, "autoindex": true,
	"deny": true, "allow": true, "error_page": true, "client_max_body_size": true,
	"client_body_buffer_size": true, "index": true, "satisfy": true,
	"auth_basic": true, "auth_basic_user_file": true,
}

// validateRewriteRules checks each non-empty, non-comment line is an
// allowlisted nginx directive. Block-scoped `if (...)` lines are validated
// loosely (their inner directives on following lines still go through this).
// forbiddenDirectiveTokens must never appear anywhere in a snippet line:
// they would let one line smuggle new server-block directives (audit S6 —
// the first-token-only check left the rest of the line unvalidated).
var forbiddenDirectiveTokens = []string{";", "{", "}", "`", "proxy_pass", "fastcgi_pass", "include", "root ", "alias ", "daemon", "error_log", "access_log", "listen ", "server_name", "location "}

// varRefRe constrains nginx variable references to well-known capture refs
// and safe built-ins (rewrite backrefs like $1, $uri, $args, ...).
var varRefRe = regexp.MustCompile(`^(\d|uri|args|query_string|request_uri|host|scheme|request_method|remote_addr|https|server_port|http_[a-z0-9_]+)`)

func validateRewriteRules(rules string) error {
	for _, raw := range strings.Split(rules, "\n") {
		line := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(raw), ";"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		head := fields[0]
		if !nginxDirectiveRe.MatchString(head) || !nginxAllowedDirectives[head] {
			return errBadRule(line)
		}
		// The whole line is validated, not just the first token (audit S6).
		if err := validateDirectiveLine(line); err != nil {
			return err
		}
	}
	return nil
}

func validateDirectiveLine(line string) error {
	for _, tok := range forbiddenDirectiveTokens {
		if strings.Contains(line, tok) {
			return errBadRule(line)
		}
	}
	for _, seg := range strings.Split(line, "$")[1:] {
		if seg == "" || !varRefRe.MatchString(seg) {
			return errBadRule(line)
		}
	}
	return nil
}

type ruleError string

func (e ruleError) Error() string { return string(e) }

func errBadRule(line string) error {
	return ruleError("unsupported directive in rewrite rules: " + line)
}

// GET .../websites/{id}/config
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
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
	cfg, err := h.Configs.Get(r.Context(), ws.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, cfg)
}

// PUT .../websites/{id}/config/rewrite {"rewrite_rules": "..."}
// Saves the snippet and re-enqueues the provision job so the agent re-renders
// the vhost with the rules included (validated with nginx -t before reload).
func (h *Handler) SetRewriteRules(w http.ResponseWriter, r *http.Request) {
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
	var req struct {
		RewriteRules string `json:"rewrite_rules"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if len(req.RewriteRules) > 16384 {
		httpapi.RespondError(w, httpapi.ErrValidation("rewrite rules too large (max 16KB)"))
		return
	}
	if err := validateRewriteRules(req.RewriteRules); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	cfg, err := h.Configs.SetRules(r.Context(), ws.ID, req.RewriteRules)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "website.rewrite_rules_updated", "website", ws.ID.String(), nil)

	// Reconcile the vhost so the rules go live (only for ready sites).
	if ws.Status == StatusReady || ws.Status == StatusFailed {
		payload, apiErr := h.buildDesiredPayload(r.Context(), ws, orgID, ws.UnixUser, ws.RuntimeVersion)
		if apiErr == nil {
			_, _ = h.Jobs.EnqueueIdempotent(r.Context(), ws.ServerID, &ws.ID, jobs.TypeProvisionWebsite, payload, "provision_website_"+ws.ID.String())
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, cfg)
}
