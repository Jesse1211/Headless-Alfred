import { test, expect, Page } from '@playwright/test'

// Refresh-parity test for the server-as-truth refactor.
// Assumes:
//   - alfred-server running on http://localhost:8080
//   - Vite dev server running on http://localhost:5173
//   - login: admin / admin
//   - claude CLI in the backend's PATH, authenticated
//
// The contract this verifies: visible strings rendered after a turn
// finishes (cost, total elapsed, per-tool elapsed, tool decision)
// must reappear unchanged after a hard page reload. If any string
// differs, the refactor's central invariant has regressed.

const BACKEND = process.env.ALFRED_BACKEND_URL || 'http://localhost:8080'
const ALFRED_USER = process.env.ALFRED_USER || 'admin'
const ALFRED_PASSWORD = process.env.ALFRED_PASSWORD || 'admin'

let cachedToken = ''

async function login(page: Page): Promise<string> {
  if (cachedToken) return cachedToken
  const r = await page.request.post(`${BACKEND}/api/login`, {
    data: { user: ALFRED_USER, password: ALFRED_PASSWORD },
  })
  const { token } = await r.json()
  cachedToken = token
  return token
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

// Cleanup pw-parity-* sessions before the run.
test.beforeAll(async ({ request }) => {
  const r = await request.post(`${BACKEND}/api/login`, {
    data: { user: ALFRED_USER, password: ALFRED_PASSWORD },
  })
  const { token } = await r.json()
  cachedToken = token
  const list = (await request
    .get(`${BACKEND}/api/sessions`, {
      headers: { Authorization: `Bearer ${token}` },
    })
    .then((r) => r.json())) as Array<{ id: string; name: string }>
  for (const s of list) {
    if (s.name.startsWith('pw-parity-')) {
      await request
        .delete(`${BACKEND}/api/sessions/${s.id}`, {
          headers: { Authorization: `Bearer ${token}` },
        })
        .catch(() => {})
    }
  }
})

test('refresh preserves cost, total elapsed, tool elapsed', async ({ page }) => {
  test.setTimeout(120_000)
  const token = await login(page)
  // Create a fresh session via the API so we don't depend on the
  // sidebar's last-selected behavior.
  const sessionRes = await page.request.post(`${BACKEND}/api/sessions`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { name: `pw-parity-${Date.now()}` },
  })
  const { id: sessionID } = await sessionRes.json()

  await loginUI(page, token)
  // Click the session in the sidebar.
  await page.locator(`.sessions-sidebar li:has-text("pw-parity-")`).first().click()
  await enterClaudeUI(page)

  // Send a prompt that triggers a Bash tool call (cheap + fast).
  await sendPrompt(page, 'run `pwd` using the Bash tool and tell me the result in one line')

  // Wait for the turn footer (cost + elapsed) to appear — the result
  // event fires it.
  const footer = page.locator('.claude-turn__footer').last()
  await expect(footer).toBeVisible({ timeout: 60_000 })

  // Snapshot visible strings.
  async function snapshot() {
    return {
      footerText: (await footer.textContent())?.trim() ?? '',
      toolElapsed:
        (await page.locator('.claude-tool__elapsed').first().textContent())?.trim() ?? '',
      toolStatus:
        (await page.locator('.claude-tool__status').first().textContent())?.trim() ?? '',
    }
  }

  const before = await snapshot()
  expect(before.footerText.length).toBeGreaterThan(0)
  expect(before.toolElapsed.length).toBeGreaterThan(0)

  // Hard reload.
  await page.reload()
  // Re-enter Claude after reload — useClaudeStateLoader hydrates the
  // turns from the new /claude-state endpoint.
  await expect(page.locator('.workspace')).toBeVisible({ timeout: 10_000 })
  // The session should re-attach; wait for the existing turn footer.
  await expect(footer).toBeVisible({ timeout: 30_000 })

  const after = await snapshot()
  // Visible-string parity is the contract.
  expect(after.footerText).toBe(before.footerText)
  expect(after.toolElapsed).toBe(before.toolElapsed)
  expect(after.toolStatus).toBe(before.toolStatus)

  // Cleanup.
  await page.request.delete(`${BACKEND}/api/sessions/${sessionID}`, {
    headers: { Authorization: `Bearer ${token}` },
  })
})
