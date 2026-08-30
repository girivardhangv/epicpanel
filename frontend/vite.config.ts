import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// Dev proxy keeps browser traffic same-origin so httpOnly session cookies work.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      "/api": {
        target: process.env.VITE_API_ORIGIN ?? "http://127.0.0.1:8080",
        changeOrigin: false,
      },
      "/healthz": {
        target: process.env.VITE_API_ORIGIN ?? "http://127.0.0.1:8080",
        changeOrigin: false,
      },
    },
  },
});
