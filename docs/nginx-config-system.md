# Context-Aware Nginx Configuration System (ADR-068)

Status: Implemented (branch `feat/nginx-config-system`)
Replaces: flat "Rewrite Rules & Custom Config" allowlist (migrations 0018 + audit S6 hardening)

## 1. Current architecture (verified before redesign)

```
UI (SiteDetail.tsx textarea)
  → PUT /v1/organizations/{org}/websites/{id}/config/rewrite   {rewrite_rules: "..."}
      websites/config_handler.go
        validateRewriteRules()      flat first-token allowlist + whole-line
                                  smuggling filter + $var ref regex; 16KB cap
        Configs.SetRules()          website_configs.rewrite_rules (TEXT, 0018)
        audit + re-enqueue provision_website job (idempotent key)
  → agent claims job
      DesiredPayload.RewriteRules  (websites/store.go)
      NginxProvider.Ensure()       agent/nginx.go
        RenderVhost(): server blocks per docroot group (:80 plain,
                        :80 redirect, :443 ssl); common = commonLocations()
                        + renderRewriteRules() → user snippet at SERVER
                        level of EVERY server block
        sanitizeRewriteLine()       agent-side re-validation (defense in
                        depth, webserver.go shared rewrite core) — bad
                        lines dropped loudly, never rendered
        SwapValidated(nginx -t)     atomic write + candidate validation +
                        restore-on-failure (atomic.go) → reload
Apache provider: snippet inside managed <IfModule mod_rewrite.c>
OLS provider:    only nginx-style `rewrite` lines mapped into context /
```

Gaps: no location blocks (the `location / { try_files ... }` WordPress/Laravel
pattern is structurally impossible), no context awareness, no structured
sections (headers/IP access/error pages/upload size), no conflict detection,
no config versioning/rollback, one textarea UI.

## 2. New architecture

### 2.1 Shared core: `internal/nginxcfg`

One package used by BOTH the control plane (save-time validation) and the
agent (render-time re-validation — defense in depth preserved). Contains:

**Directive registry** — every directive modeled with allowed contexts,
argument shape and security level (per nginx.com directive index; the
authoritative per-version gate remains `nginx -t` on the candidate):

```go
type Ctx uint8           // CtxServer | CtxLocation | CtxIf
type Level uint8         // SAFE | USER | RESTRICTED | MANAGED

type Directive struct {
    Name     string
    Contexts uint8          // bitmask
    Level    Level
    Args     ArgSpec        // min/max + arg kinds (regex, size, path, ...)
}
```

Only `Level: USER` directives are accepted from customers; RESTRICTED/
MANAGED (`include`, `root`, `alias`, `listen`, `server_name`, `location` as
raw text, `fastcgi_pass`, `auth_request`, `perl/set/lua/js`, `access_log`,
`error_log`, `daemon`, `upstream`, `map`) produce the friendly section-25
error messages ("Detected a location block — use the Locations section…").

**Line parser** — tokenizes one line (nginx-style quoting not supported:
single-token arguments only), classifies:
- directive line
- `if (...) {` block opener (contents validated at CtxIf)
- stray `{` / `}` / `;` → smuggling rejection (audit S6 semantics preserved)

**Validator** — `ValidateSnippet(text, ctx)` walks lines against the
registry: directive known? allowed in THIS context? argument shape?
forbidden tokens anywhere (`;` `{` `}` backtick + RESTRICTED names)?
variable references limited to the safe set (`$1..$9`, `$uri`, `$args`,
`$request_uri`, `$host`, `$scheme`, `$request_method`, `$remote_addr`,
`$https`, `$server_port`, `$query_string`, `$http_*`; `$epicpanel_*`
never — protects the bandwidth-accounting + site-ID stamping)?

**Structured config model** (`SiteConfig`, JSON, schema 2):

```go
type SiteConfig struct {
    ServerDirectives  string            // Advanced: raw server-context lines
    RootLocation      *RootLocationCfg  // override for managed location /
    Locations         []CustomLocation  // generated location blocks
    Headers           []HeaderRule      // add_header {name, value, always}
    IPAccess          *IPAccess         // allow/deny {cidr, action} + default
    ErrorPages        []ErrorPageRule   // status → internal URI
    ClientMaxBodySize string            // validated nginx size ("32m")
    AssetCaching      *AssetCaching     // asset classes + expires → location
    Redirects         []PathRedirect    // path/domain redirects {from,to,code}
}
```

The renderer (agent) generates all wrappers — users never type
`location … { }`.

**Conflict detection** (`ValidateSiteConfig`, control plane + agent):
- duplicate custom location (same type+matcher)
- custom location shadowing a MANAGED one: prefix `/` (that is the
  RootLocation section's job), `^~ /.well-known/acme-challenge/`,
  `= /epicpanel-busy.html` / `= /epicpanel-notfound.html`, regex matching
  the managed PHP handler `\.php$` on PHP sites, `~ /.` dotfile pattern
- error_page codes colliding with panel defaults → user rule wins
  deterministically (managed default for that code is dropped)
- try_files only on non-proxy serving modes
- proxy_pass targets: loopback ONLY (`127.0.0.1:port`, `localhost:port`,
  `[::1]:port`), no variables, no unix: sockets; the CONTROL PLANE
  additionally rejects ports owned by a DIFFERENT website (app/backend
  port allocation) — site A can never route to site B's process

**Multi-tenant invariants** (spec §11/§28):
- `auth_basic_user_file` must resolve under the site's own tree
- error_page URIs: internal, leading `/`, no `..`, no variables
- location matchers: no whitespace/variables/braces; regex = single token
- no directive can touch root/alias/include/listen/server_name → a site
  can only ever configure ITSELF (its own server block, own docroot space)
- final gate: candidate `nginx -t` before reload, restore on failure
  (existing SwapValidated path, unchanged)

### 2.2 Storage + versioning (migration 0056)

```sql
ALTER TABLE website_configs
  ADD COLUMN config_json JSONB NOT NULL DEFAULT '{}',
  ADD COLUMN version    INT   NOT NULL DEFAULT 1;
CREATE TABLE website_config_versions (
  website_id UUID REFERENCES websites(id) ON DELETE CASCADE,
  version    INT,
  config_json JSONB NOT NULL,
  created_by UUID,
  created_at TIMESTAMPTZ DEFAULT now(),
  PRIMARY KEY (website_id, version)
);
```

`rewrite_rules` TEXT column is KEPT (legacy view + Apache/OLS providers
keep consuming the string). Rollback = copy an old version forward as a
new version (append-only history, last 20 kept). No destructive content
migration: a site with only legacy rules keeps serving exactly as before
until the owner saves a structured config (server_directives is seeded
from rewrite_rules on first structured save).

### 2.3 API (existing desired-state conventions)

```
GET  .../websites/{id}/config              → {rewrite_rules, config, version, updated_at}
PUT  .../websites/{id}/config              → full SiteConfig replace; validate →
                                             version++ → reconcile job
POST .../websites/{id}/config/validate     → dry-run validation, saves nothing
GET  .../websites/{id}/config/versions     → history (last 20)
POST .../websites/{id}/config/rollback     → {to_version} → new version + reconcile
PUT  .../websites/{id}/config/rewrite      → LEGACY, unchanged contract; maps onto
                                             config.server_directives
```

No separate "apply" endpoint: EpicPanel is desired-state — saving the
config enqueues the idempotent provision job which re-renders the vhost.
Deployment safety (spec §14) is the EXISTING agent path: render → compare
(skip reload when byte-identical) → SwapValidated(nginx -t) → enable →
reload → restore-on-failure.

### 2.4 Agent rendering (layered, deterministic)

```
server {
    # EPICPANEL MANAGED — platform-owned (logs, site-ID stamp, ssl, root…)
    # EPICPANEL USER CONFIG — validated user layer:
    client_max_body_size / add_header… / allow+deny / error_page…
    redirects (rewrite) / raw server directives
    location / { managed try_files (or RootLocation override) + extras }
    location ~ \.php$ { managed FPM handler }        (PHP sites)
    location / { managed proxy block + safe extras } (proxy sites)
    # generated custom locations (validated bodies)
}
```

Rendering rules:
- user layer renders AFTER managed directives at server level, inside a
  marked section; agent re-validates every structured field + raw line via
  `nginxcfg` before render (never trusts stored content)
- suspended/terminated/quota stubs render NO user layer (unchanged)
- server-level `access_log` stays platform-owned (billing stream);
  `add_header`/`expires` in locations never suppress accounting
- Apache/OLS providers keep today's behavior (legacy string via their own
  token sets) — structured sections are nginx-only; the UI only offers
  them for `web_server: nginx` sites

### 2.5 Frontend

SiteDetail.tsx: the textarea card becomes a tabbed "Web Server
Configuration" card (Overview+Presets | Redirects | Locations | Headers |
Access | Error Pages | Advanced). Presets (Laravel, WordPress, SPA,
Static, Security Headers, Asset Caching) materialize into the structured
fields — editable, never opaque blobs. Advanced tab keeps the raw
server-context editor with context-aware errors. Customer app gains the
same editor component.

## 3. Backward compatibility

- legacy `PUT config/rewrite` keeps its exact contract (validated as
  server-context snippet; writes server_directives)
- sites with only `rewrite_rules` render identically (agent falls back to
  the legacy string when `config_json` is empty)
- Apache/OLS sites: no behavior change
- existing deny/allow/add_header/error_page/expires snippets in the wild
  remain valid (same directives, now with context tracking)
