import { defineConfig } from '@playwright/test'

// Playwright config for the front-end e2e tests. Assumes the
// backend is already running on :8080 (started manually with
// /tmp/alfred-server) and Vite dev on :5173. We don't auto-spawn
// them here — the goal is to verify the live system, not a
// disposable one.
export default defineConfig({
  testDir: './e2e',
  timeout: 30_000,
  expect: { timeout: 5_000 },
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  use: {
    baseURL: 'http://localhost:5173',
    screenshot: 'only-on-failure',
    trace: 'retain-on-failure',
  },
  projects: [
    { name: 'chromium', use: { browserName: 'chromium' } },
  ],
})
