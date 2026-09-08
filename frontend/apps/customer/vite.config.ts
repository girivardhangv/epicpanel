import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

// apps/customer — the cPanel experience (Phase 5). Shares packages/* with the
// admin app (Phase 6). Dev proxy mirrors the root app: /v1 -> 127.0.0.1:8080.
export default defineConfig({
  root: __dirname,
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
  server: {
    port: 5174,
    allowedHosts: true,
    proxy: {
      '/v1': { target: 'http://127.0.0.1:8080', changeOrigin: true, ws: true },
      '/healthz': { target: 'http://127.0.0.1:8080', changeOrigin: true },
    },
  },
  preview: {
    allowedHosts: true,
  },
})
