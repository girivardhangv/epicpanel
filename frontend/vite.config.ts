import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src'),
      '@epicpanel/core': path.resolve(__dirname, 'packages/core/index.ts'),
      '@epicpanel/ui': path.resolve(__dirname, 'packages/ui/index.ts'),
      '@epicpanel/icons': path.resolve(__dirname, 'packages/icons/index.ts'),
      '@epicpanel/charts': path.resolve(__dirname, 'packages/charts/index.tsx'),
      '@epicpanel/tables': path.resolve(__dirname, 'packages/tables/index.tsx'),
      '@epicpanel/forms': path.resolve(__dirname, 'packages/forms/index.tsx'),
      '@epicpanel/design-system': path.resolve(__dirname, 'packages/design-system/index.ts'),
    },
  },
  server: {
    port: 5173,
    // The panel is reached through arbitrary hostnames (domains pointed at
    // the VPS) — never block any Host header in dev/preview.
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
