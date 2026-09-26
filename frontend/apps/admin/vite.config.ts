import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from 'tailwindcss'
import autoprefixer from 'autoprefixer'
import path from 'path'
import baseTailwind from '../../tailwind.config.js'

// apps/admin — the WHM experience (Phase 6). Shares packages/* with the
// customer app (Phase 5): ONE design system, TWO experiences. Dev proxy
// mirrors the other apps: /v1 -> 127.0.0.1:8080 (port 5175), overridable
// with EPICPANEL_API (e.g. EPICPANEL_API=http://10.0.0.9:8080).

// Backend target for the dev/preview proxy. Process env wins so CI and
// one-off runs can point the WHM at any API without touching the config.
const API_TARGET = process.env.EPICPANEL_API || 'http://127.0.0.1:8080'

export default defineConfig({
  root: __dirname,
  // Served by the API binary under the same origin as the platform app
  // (:8080/admin/) — base must match the mount point or asset URLs break.
  base: '/admin/',
  plugins: [react()],
  resolve: {
    alias: {
      '@epicpanel/core': path.resolve(__dirname, '../../packages/core/index.ts'),
      '@epicpanel/ui': path.resolve(__dirname, '../../packages/ui/index.ts'),
      '@epicpanel/icons': path.resolve(__dirname, '../../packages/icons/index.ts'),
      '@epicpanel/charts': path.resolve(__dirname, '../../packages/charts/index.tsx'),
      '@epicpanel/tables': path.resolve(__dirname, '../../packages/tables/index.tsx'),
      '@epicpanel/forms': path.resolve(__dirname, '../../packages/forms/index.tsx'),
      '@epicpanel/design-system': path.resolve(__dirname, '../../packages/design-system/index.ts'),
    },
  },
  css: {
    // Self-contained PostCSS pipeline: theme comes from the shared
    // frontend/tailwind.config.js (verbatim import), but `content` is
    // overridden with absolute globs so the build is cwd-independent
    // (`cd apps/admin && npx vite build` and the root npm script both
    // work; Tailwind resolves relative globs against the process cwd).
    postcss: {
      plugins: [
        tailwindcss({
          ...baseTailwind,
          content: [
            path.resolve(__dirname, 'index.html'),
            path.resolve(__dirname, './*.tsx'),
            path.resolve(__dirname, './pages/**/*.{ts,tsx}'),
            path.resolve(__dirname, '../../packages/**/*.{ts,tsx}'),
          ],
        }),
        autoprefixer(),
      ],
    },
  },
  build: {
    rollupOptions: {
      output: {
        // Long-term-cacheable vendor split: framework chunks change far less
        // often than app code, so page chunks stay small and re-download
        // rarely. Route chunks come from React.lazy in App.tsx / routes.*.tsx.
        manualChunks(id) {
          if (!id.includes('node_modules')) return undefined
          if (/[\\/]node_modules[\\/](react|react-dom|scheduler)[\\/]/.test(id)) return 'vendor-react'
          if (/[\\/]node_modules[\\/](react-router|react-router-dom)[\\/]/.test(id)) return 'vendor-router'
          if (id.includes('lucide-react')) return 'vendor-icons'
          return 'vendor'
        },
      },
    },
  },
  server: {
    port: 5175,
    allowedHosts: true,
    proxy: {
      '/v1': {
        target: API_TARGET,
        changeOrigin: true,
        ws: true,
        headers: { Origin: 'http://localhost:5173' },
      },
      '/healthz': { target: API_TARGET, changeOrigin: true },
    },
  },
  preview: {
    allowedHosts: true,
  },
})
