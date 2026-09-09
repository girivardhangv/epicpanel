/**
 * EpicPanel installer CDN worker — get.epichostly.com
 *
 * Serves install.sh for the curl|bash flow and proxies release binaries for
 * downloads.epichostly.com. Design:
 *   - INSTALLER source of truth: R2 bucket (or GitHub raw as fallback). The
 *     worker caches at the edge (1 min) so pushing a new installer is fast.
 *   - BINARIES are proxied from your origin/pointer (set BINARY_ORIGIN) with
 *     long cache; `latest` resolves via a small manifest in R2
 *     (releases/latest.json -> { "version": "v1.0.0" }).
 *
 * Setup (once):
 *   1. wrangler r2 bucket create epicpanel-releases
 *   2. wrangler r2 object put epicpanel-releases/installer/install.sh --file install.sh
 *   3. wrangler r2 object put epicpanel-releases/releases/v1.0.0/epicpanel-api-linux-amd64 --file backend/bin/epicpanel-api   (per OS/arch)
 *   4. wrangler r2 object put epicpanel-releases/releases/latest.json --file latest.json
 *   5. wrangler deploy  (routes bound to get.epichostly.com + downloads.epichostly.com)
 *
 * The installer auto-detects the channel via EPICPANEL_INSTALL_URL: when the
 * worker serves it, it sets that env var below so `epicpanel-update` re-pulls
 * from the same place.
 */

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    const host = url.hostname;

    // ---------------- get.epichostly.com ----------------
    if (host === env.GET_HOST) {
      const installer = await env.RELEASES.get("installer/install.sh");
      if (!installer) {
        return new Response("installer missing from R2 (installer/install.sh)", { status: 404 });
      }
      const body = await installer.text();
      const headers = {
        "Content-Type": "text/x-shellscript; charset=utf-8",
        "Cache-Control": "public, max-age=60",
        // The updater re-pulls this exact URL:
        "X-EpicPanel-Install-URL": `https://${host}`,
      };
      return new Response(body, { headers });
    }

    // ---------------- downloads.epichostly.com ----------------
    if (host === env.DOWNLOADS_HOST) {
      // /latest/<file> resolves latest.json then redirects to the versioned key.
      // /v1.0.0/<file> serves the versioned object directly.
      const path = url.pathname.replace(/^\/+/, ""); // e.g. latest/epicpanel-api-linux-amd64
      const [channel, file] = path.split("/");

      if (!file) {
        return new Response("usage: /latest/<binary-file> or /vX.Y.Z/<binary-file>", { status: 400 });
      }

      let version = channel;
      if (channel === "latest") {
        const manifest = await env.RELEASES.get("releases/latest.json");
        if (!manifest) return new Response("latest.json missing", { status: 404 });
        version = JSON.parse(await manifest.text()).version; // e.g. "v1.0.0"
      }

      const key = `releases/${version}/${file}`;
      const obj = await env.RELEASES.get(key);
      if (!obj) return new Response(`not found: ${key}`, { status: 404 });

      const headers = {
        "Content-Type": "application/octet-stream",
        "Cache-Control": channel === "latest"
          ? "public, max-age=60"
          : "public, max-age=31536000, immutable", // versioned = immutable
      };
      return new Response(obj.body, { headers });
    }

    return new Response("not found", { status: 404 });
  },
};

// wrangler.toml (fill in your hosts):
//
// name = "epicpanel-installer"
// main = "worker.js"
// compatibility_date = "2026-09-01"
// routes = [
//   { pattern = "get.epichostly.com", custom_domain = true },
//   { pattern = "downloads.epichostly.com", custom_domain = true },
// ]
//
// [[r2_buckets]]
// binding = "RELEASES"
// bucket_name = "epicpanel-releases"
//
// [vars]
// GET_HOST = "get.epichostly.com"
// DOWNLOADS_HOST = "downloads.epichostly.com"
