import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'

// Vite dev server (port 5173) proxies /api and /ws to a backend.
//
// Default: local backend on http://localhost:8080 (the usual dev
// loop: run `go run ./cmd/alfred-server` in one terminal,
// `npm run dev` in another).
//
// Override: set VITE_BACKEND_URL to point at a remote backend. The
// most useful case is pointing at the deployed oracle k3s pod via
// the ssh tunnel — you get Vite HMR on the frontend while talking
// to the production-deployed backend:
//
//   # Terminal 1: tunnel oracle → local 8888
//   ssh -fN -L 8888:127.0.0.1:80 oracle
//
//   # Terminal 2: dev server pointing at the tunnel
//   VITE_BACKEND_URL=http://alfred.local:8888 npm run dev
//
// Note: the tunnel target hostname needs to be a name Traefik
// accepts as Host: alfred.local works because the Ingress is
// configured for it; plain localhost would 404 at Traefik. The
// vite proxy preserves the Host header from the request URL by
// default, so as long as the client browser hits 5173 with the
// right Host (it does — same-origin requests use the page host),
// everything lines up.
//
// VITE_BACKEND_URL is server-only (loaded via loadEnv with no
// VITE_ prefix prefix filter — but here we only consume it inside
// the Vite config, never embed it into the client bundle).
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const backend = env.VITE_BACKEND_URL || 'http://localhost:8080'
  // Header rewriting for both HTTP and WS proxies:
  //
  // - Host: With Traefik in front (deployed mode), the Ingress
  //   matches on Host: alfred.local. Without rewriting, the client
  //   sends Host: localhost:5173 → Traefik returns 404 (HTTP) or
  //   RSTs the connection (WS). We override Host to the hostname-
  //   ONLY part of the target (alfred.local, no :8888). Traefik
  //   strips the port itself, but more importantly, the backend's
  //   WebSocket Upgrader checks Origin == "scheme://" + r.Host,
  //   and r.Host doesn't include the port the client connected on
  //   if it was rewritten by the proxy chain — so we keep both
  //   header values port-free and they line up.
  //
  // - Origin: alfred-server's WebSocket Upgrader enforces same-
  //   origin via CheckOrigin (Origin must equal scheme://host).
  //   Without rewriting, Origin says http://localhost:5173 and the
  //   check fails ("request origin not allowed by Upgrader.
  //   CheckOrigin" → 403). We set Origin to match the rewritten
  //   Host. Safe in dev because the proxy is bound to 127.0.0.1.
  const backendURL = new URL(backend)
  const backendHostNoPort = backendURL.hostname
  const backendOrigin = `${backendURL.protocol}//${backendHostNoPort}`
  // Resolve target to 127.0.0.1 to avoid DNS recursion through
  // the host resolver (which could short-circuit alfred.local to
  // localhost depending on /etc/hosts order). The Host header
  // override (above) keeps Traefik happy with alfred.local.
  const targetHost = backendURL.hostname === 'localhost' ? 'localhost' : '127.0.0.1'
  const httpTarget = `${backendURL.protocol}//${targetHost}:${backendURL.port || (backendURL.protocol === 'https:' ? 443 : 80)}`
  const wsTarget = httpTarget.replace(/^http/, 'ws')
  return {
    plugins: [react()],
    server: {
      port: 5173,
      proxy: {
        '/api': {
          target: httpTarget,
          changeOrigin: true,
          // configure() runs on proxy init; we attach a proxyReq
          // handler that rewrites Host + Origin per request. Doing
          // this via the configure callback (rather than the
          // top-level `headers` option) is the only path that works
          // for WS upgrades — `headers:` interferes with the
          // upgrade response forwarding in http-proxy-middleware.
          configure: (proxy) => {
            proxy.on('proxyReq', (proxyReq) => {
              proxyReq.setHeader('Host', backendHostNoPort)
              proxyReq.setHeader('Origin', backendOrigin)
            })
          },
        },
        '/ws': {
          target: wsTarget,
          ws: true,
          changeOrigin: true,
          configure: (proxy) => {
            proxy.on('proxyReqWs', (proxyReq) => {
              proxyReq.setHeader('Host', backendHostNoPort)
              proxyReq.setHeader('Origin', backendOrigin)
            })
          },
        },
      },
    },
    build: {
      outDir: 'dist',
      emptyOutDir: true,
    },
  }
})
