import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

// ESM-safe config dir (Vite 8's native config loader does not provide __dirname).
const root = import.meta.dirname

// API target for the dev/preview proxy — override with EPICPANEL_API.
const apiTarget = process.env.EPICPANEL_API ?? 'http://127.0.0.1:8081'

export default defineConfig({
  plugins: [react()],
  build: {
    rollupOptions: {
      output: {
        // Stable vendor splits (function form — Vite 8 is rolldown-backed,
        // which only supports manualChunks as a predicate, not an object).
        manualChunks: (id: string) => {
          if (!id.includes('node_modules')) return null
          // React + router: the shared runtime every route needs.
          if (/[\\/]node_modules[\\/](react|react-dom|react-router|react-router-dom|scheduler)[\\/]/.test(id)) {
            return 'vendor'
          }
          // xterm.js is only used by the per-site Terminal page.
          if (/[\\/]node_modules[\\/]@xterm[\\/]/.test(id)) {
            return 'terminal'
          }
          // NOTE: no recharts/d3 (packages/charts is hand-rolled SVG), and no
          // markdown/highlight dependencies exist yet — add groups here if that
          // changes.
          return null
        },
      },
    },
  },
  resolve: {
    alias: {
      '@': path.resolve(root, 'src'),
      '@epicpanel/core': path.resolve(root, 'packages/core/index.ts'),
      '@epicpanel/ui': path.resolve(root, 'packages/ui/index.ts'),
      '@epicpanel/icons': path.resolve(root, 'packages/icons/index.ts'),
      '@epicpanel/charts': path.resolve(root, 'packages/charts/index.tsx'),
      '@epicpanel/tables': path.resolve(root, 'packages/tables/index.tsx'),
      '@epicpanel/forms': path.resolve(root, 'packages/forms/index.tsx'),
      '@epicpanel/design-system': path.resolve(root, 'packages/design-system/index.ts'),
    },
  },
  server: {
    port: 5173,
    // The panel is reached through arbitrary hostnames (domains pointed at
    // the VPS) — never block any Host header in dev/preview.
    allowedHosts: true,
    proxy: {
      '/v1': { target: apiTarget, changeOrigin: true, ws: true },
      '/healthz': { target: apiTarget, changeOrigin: true },
    },
  },
  preview: {
    allowedHosts: true,
  },
})
