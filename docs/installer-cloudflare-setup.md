# Cloudflare installer setup — complete walkthrough

Goal: `curl -fsSL https://get.epichostly.in | bash` installs EpicPanel on any
fresh Ubuntu/Debian VPS, and `downloads.epichostly.in/latest/<binary>` serves
your release binaries. Both are a single Cloudflare Worker + one R2 bucket.
Total cost: $0 (Workers free tier + R2 storage-only pricing, no egress fees).

---

## Part 0 — What you need

| Thing | Notes |
|---|---|
| Cloudflare account | free plan is enough |
| Domain `epichostly.in` | **added as a zone in Cloudflare** (nameservers pointed at Cloudflare) |
| Node.js 18+ | on YOUR machine (not the VPS) — runs `wrangler` |
| Built release binaries | from this repo: `cd backend && ./build-release.sh` |

How the pieces fit:

```
VPS (curl) ──▶ get.epichostly.in ────▶ Worker ──▶ R2 "epicpanel-releases"
                                                 ├─ installer/install.sh
installer runs ──▶ downloads.epichostly.in ─────▶ └─ releases/latest.json
                                                    releases/vX.Y.Z/epicpanel-{api,agent}-linux-{amd64,arm64}
```

The worker resolves `latest` via `releases/latest.json` → users always get
your newest release with `sudo epicpanel-update`.

---

## Part 1 — Cloudflare + wrangler (one-time, on your machine)

```bash
# 1. Install the deploy tool
npm install -g wrangler

# 2. Log in (opens a browser; on a headless box it prints a URL)
wrangler login
wrangler whoami        # verify
```

## Part 2 — Create the R2 bucket + upload artifacts

```bash
# 3. The bucket (first-time R2 use asks you to enable it in the dashboard)
wrangler r2 bucket create epicpanel-releases

# 4. Upload the installer script (from the repo root)
wrangler r2 object put epicpanel-releases/installer/install.sh --file install.sh

# 5. Build + upload release binaries (amd64 + arm64, api + agent)
cd backend && ./build-release.sh upload v1.0.0
#   → builds bin/epicpanel-{api,agent}-linux-{amd64,arm64}
#   → uploads to releases/v1.0.0/… and writes releases/latest.json = {"version":"v1.0.0"}
cd ..
```

Verify what's in the bucket:

```bash
wrangler r2 object get epicpanel-releases/releases/latest.json --pipe
# → {"version":"v1.0.0"}
```

## Part 3 — Deploy the worker

The worker config lives at `deploy/cloudflare/wrangler.toml` (already set to
`get.epichostly.in` + `downloads.epichostly.in`).

```bash
cd deploy/cloudflare
wrangler deploy
```

First deploy will ask to attach the **custom domains** — say yes. This only
works because `epichostly.in` is a zone in the same Cloudflare account; the
DNS records for both hostnames are created for you automatically (no manual
A records needed — Cloudflare proxies them to the worker).

If you'd rather click: Dashboard → Workers & Pages → epicpanel-installer →
Settings → Domains & Routes → Add → Custom domain (do it for both hostnames).

## Part 4 — Test everything

```bash
# The installer (should print the shell script):
curl -fsSL https://get.epichostly.in | head -20

# Binary resolution (should redirect/stream a binary):
curl -fsSI https://downloads.epichostly.in/latest/epicpanel-api-linux-amd64 | head -5

# Full end-to-end on a FRESH VPS:
curl -fsSL https://get.epichostly.in | bash
```

The installer prints the panel URL + one-time setup link when done.

---

## Part 5 — Releasing updates (the ongoing loop)

```bash
# 1. Change code, bump version, build + upload + flip latest:
cd backend && ./build-release.sh upload v1.0.1

# 2. Every installed panel updates with:
sudo epicpanel-update
```

Installer script changes (not binaries) are a separate upload:

```bash
wrangler r2 object put epicpanel-releases/installer/install.sh --file install.sh
# cached 60s at the edge — new installs get it almost immediately
```

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| `curl` returns 522/1016 | the hostname isn't a worker custom domain — check Workers → Settings → Domains; the zone must be on the SAME account |
| `installer missing from R2` | you skipped Part 2 step 4 — put `installer/install.sh` |
| `not found: releases/vX/latest/...` | `latest.json` missing or malformed — re-run `./build-release.sh upload vX` |
| 404 on a binary | filename must match exactly: `releases/<version>/epicpanel-api-linux-amd64` (dash before linux, arch suffix) |
| `wrangler deploy` route error | another record (A/CNAME) already exists on that hostname — delete it in DNS first |
| Worker deploy asks about compatibility date | accept the default; `2026-09-01` is set in wrangler.toml |

## Security notes

- The installer is **plain HTTP text** over HTTPS — inspect it: `curl -fsSL https://get.epichostly.in | less`
- Binaries are served with SHA256SUMS published alongside (backend/bin/SHA256SUMS) — pin a version in production if you want bit-reproducible installs:
  `EPICPANEL_DOWNLOAD_BASE=https://downloads.epichostly.in/v1.0.0 curl -fsSL https://get.epichostly.in | bash`
- R2 tokens stay in `wrangler login` state on your machine; the worker uses Cloudflare's internal binding (no keys in code).
