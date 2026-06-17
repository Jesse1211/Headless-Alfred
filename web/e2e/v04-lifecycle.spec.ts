import { test, expect, Page } from '@playwright/test'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

// v0.4 long-running task / lifecycle visibility tests.
// Assumes:
//   - alfred-server running on http://localhost:8080
//   - Vite dev server running on http://localhost:5173
//   - login: admin / admin
//   - claude CLI in the backend's PATH with --include-hook-events
//     support and an authenticated session (Anthropic creds)
//
// These tests are SLOW (multi-minute) because they exercise real
// Monitor + Agent dispatches against the live CLI. Run them with:
//   npx playwright test e2e/v04-lifecycle.spec.ts --timeout=600000

const BACKEND = 'http://localhost:8080'

let cachedToken = ''

async function login(page: Page): Promise<string> {
  if (cachedToken) return cachedToken
  const r = await page.request.post(`${BACKEND}/api/login`, {
    data: { user: 'admin', password: 'admin' },
  })
  const { token } = await r.json()
  cachedToken = token
  return token
}

async function freshSession(page: Page, token: string, name: string): Promise<string> {
  const r = await page.request.post(`${BACKEND}/api/sessions`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { name: `pw-v04-${name}-${Date.now()}` },
  })
  const { id } = await r.json()
  return id
}

async function loginUI(page: Page, token: string): Promise<void> {
  await page.goto('http://localhost:5173/')
  await page.evaluate((t) => localStorage.setItem('alfred_token', t), token)
  await page.reload()
  await expect(page.locator('.workspace')).toBeVisible({ timeout: 10_000 })
}

async function enterClaudeUI(page: Page): Promise<void> {
  await page.locator('.workspace__claude-btn').click()
  await expect(page.locator('text=Start Claude')).toBeVisible()
  await page.locator('label:has-text("Chat UI")').click()
  await page.locator('button:has-text("Start")').click()
  await expect(page.locator('textarea.claude-chat__input')).toBeVisible({ timeout: 10_000 })
}

async function sendPrompt(page: Page, text: string): Promise<void> {
  const ta = page.locator('textarea.claude-chat__input')
  await ta.fill(text)
  await ta.press('Enter')
}

// Cleanup: best-effort delete pw-v04-* at startup.
test.beforeAll(async ({ request }) => {
  const r = await request.post(`${BACKEND}/api/login`, {
    data: { user: 'admin', password: 'admin' },
  })
  const { token } = await r.json()
  cachedToken = token
  const list = await request.get(`${BACKEND}/api/sessions`, {
    headers: { Authorization: `Bearer ${token}` },
  }).then(r => r.json()) as Array<{ id: string; name: string }>
  for (const s of list) {
    if (s.name.startsWith('pw-v04-')) {
      await request.delete(`${BACKEND}/api/sessions/${s.id}`, {
        headers: { Authorization: `Bearer ${token}` },
      }).catch(() => {})
    }
  }
})

// Cases land in Task 17 below.
test('placeholder so the file is picked up by testMatch', async () => {
  expect(true).toBe(true)
})
