import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.ts'],
    // Playwright specs live in ./e2e and are run by `npm run test:e2e`.
    // Vitest would try to collect them and fail with "test.describe()
    // not expected here" since they use Playwright's globals.
    exclude: ['node_modules', 'dist', 'e2e/**'],
  },
})
