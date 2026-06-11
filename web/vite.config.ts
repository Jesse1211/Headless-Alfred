import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Frontend dev server proxies API + WS to the local backend on :8080.
// In production the Go binary serves the built dist/ directly.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8080',
      '/ws': {
        target: 'ws://localhost:8080',
        ws: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
})
