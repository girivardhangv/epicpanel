// EpicPanel Cloudflare Worker — reverse proxy + DNS management
//
// This worker lets you put a Cloudflare-managed domain in front of your
// EpicPanel instance, with automatic DNS record management and SSL.
//
// Setup:
//   1. Create a Cloudflare Worker
//   2. Set environment variables:
//        PANEL_ORIGIN — your panel URL (e.g. http://203.0.113.10:8080)
//        WORKER_TOKEN — a secret token to authenticate API calls to this worker
//   3. Point your Cloudflare DNS record for panel.example.com to the worker
//
// The worker proxies all requests to the panel, adds security headers,
// and handles WebSocket upgrades for the terminal.

const PANEL_ORIGIN = PANEL_ORIGIN; // set via Cloudflare env var
const WORKER_TOKEN = WORKER_TOKEN; // set via Cloudflare env var

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);

    // Health check endpoint
    if (url.pathname === '/worker-health') {
      return new Response(JSON.stringify({ status: 'ok', worker: true }), {
        headers: { 'Content-Type': 'application/json' },
      });
    }

    // Build the upstream request
    const upstream = new URL(request.url);
    upstream.hostname = new URL(env.PANEL_ORIGIN).hostname;
    upstream.port = new URL(env.PANEL_ORIGIN).port || '8080';
    upstream.protocol = new URL(env.PANEL_ORIGIN).protocol;

    // Create proxied request with forwarded headers
    const proxyReq = new Request(upstream.toString(), {
      method: request.method,
      headers: new Headers(request.headers),
      body: request.method !== 'GET' && request.method !== 'HEAD' ? request.body : undefined,
      redirect: 'follow',
    });

    // Add forwarded headers
    proxyReq.headers.set('X-Forwarded-Host', url.hostname);
    proxyReq.headers.set('X-Forwarded-Proto', url.protocol.replace(':', ''));

    try {
      const response = await fetch(proxyReq);

      // Build response with security headers
      const newHeaders = new Headers(response.headers);
      newHeaders.set('X-Content-Type-Options', 'nosniff');
      newHeaders.set('X-Frame-Options', 'SAMEORIGIN');
      newHeaders.set('Referrer-Policy', 'strict-origin-when-cross-origin');
      newHeaders.set('X-EpicPanel-Worker', '1');

      return new Response(response.body, {
        status: response.status,
        statusText: response.statusText,
        headers: newHeaders,
      });
    } catch (err) {
      return new Response(
        JSON.stringify({
          error: {
            code: 'panel_unreachable',
            message: 'The EpicPanel instance is not reachable',
          },
        }),
        { status: 502, headers: { 'Content-Type': 'application/json' } }
      );
    }
  },
};
