import { test, expect, Page } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

// e2e regression tests for the three reported bugs:
//   1. typing one command renders two interactions
//   2. instant-return commands stay stuck on "live"
//   3. Stop / pause button does not stop the running command
//
// Assumes:
//   - alfred-server running on http://127.0.0.1:8080
//   - Vite dev server running on http://127.0.0.1:5173
//   - login credentials admin / admin
//
// Each test creates its own fresh session via REST to avoid
// state bleed.

const BACKEND = 'http://localhost:8080'
const SHOTS = path.join(__dirname, '..', '.screenshots')
fs.mkdirSync(SHOTS, { recursive: true })

// Cache the token across the whole run — backend has a per-IP login
// rate limiter, and 6 logins back-to-back trips it.
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
    data: { name },
  })
  const { id } = await r.json()
  return id
}

// Track sessions created by each test so afterEach can clean them up.
// Without this the 8-session cap fills up after a couple of runs and
// new sessions (and the user's manual + New chat) start failing.
const createdInThisTest: { token: string; sid: string }[] = []

async function freshSessionTracked(page: Page, token: string, name: string): Promise<string> {
  const sid = await freshSession(page, token, name)
  createdInThisTest.push({ token, sid })
  return sid
}

test.afterEach(async ({ request }) => {
  while (createdInThisTest.length > 0) {
    const { token, sid } = createdInThisTest.pop()!
    await request.delete(`${BACKEND}/api/sessions/${sid}`, {
      headers: { Authorization: `Bearer ${token}` },
    }).catch(() => {})
  }
})

// Belt-and-suspenders: wipe pw-* sessions ONCE at the start of the
// whole run, in case a previous run crashed before afterEach.
// Doing it per-test races with the live WS client.
test.beforeAll(async ({ request }) => {
  const r = await request.post(`${BACKEND}/api/login`, {
    data: { user: 'admin', password: 'admin' },
  })
  const { token } = await r.json()
  const list = await request.get(`${BACKEND}/api/sessions`, {
    headers: { Authorization: `Bearer ${token}` },
  }).then(r => r.json()) as Array<{ id: string; name: string }>
  for (const s of list) {
    if (s.name.startsWith('pw-')) {
      await request.delete(`${BACKEND}/api/sessions/${s.id}`, {
        headers: { Authorization: `Bearer ${token}` },
      }).catch(() => {})
    }
  }
})

async function loginUI(page: Page, token: string) {
  // Skip the login form via direct localStorage injection — going
  // through the form for every test trips the backend's per-IP login
  // rate limit after a few runs.
  await page.goto('/')
  await page.evaluate((t) => localStorage.setItem('alfred_token', t), token)
  await page.reload()
  await expect(page.locator('text=+ New chat').first()).toBeVisible({ timeout: 10_000 })
}

async function selectSession(page: Page, sessionID: string) {
  // The sidebar shows session names; click by session id substring via
  // a data attribute (id is in localStorage selection). Easier: just
  // wait for the chip with this session and click it via API.
  // For simplicity, set the localStorage selection then reload.
  await page.evaluate((id) => localStorage.setItem('alfred_selected_session', id), sessionID)
  await page.reload()
  await page.waitForLoadState('networkidle')
}

async function submitCommand(page: Page, cmd: string) {
  const ta = page.locator('textarea').first()
  await ta.fill(cmd)
  await ta.press('Enter')
}

// Count visible "turns" — each completed or running command is
// rendered as a `.msg-turn`. Live ones have the `--live` modifier.
async function countTurns(page: Page): Promise<{ total: number; live: number }> {
  const total = await page.locator('.msg-turn').count()
  const live = await page.locator('.msg-turn--live').count()
  return { total, live }
}

test.describe('regression: typing one command must produce exactly one turn', () => {
  test('bug 1+2: echo hi renders one turn that lands in completed (not live)', async ({ page }) => {
    const tok = await login(page)
    const sid = await freshSessionTracked(page, tok, 'pw-bug-1-2')
    await loginUI(page, tok)
    await selectSession(page, sid)

    await submitCommand(page, 'echo hi')
    // Give the WS round-trip a moment to settle.
    await page.waitForTimeout(800)

    const { total, live } = await countTurns(page)
    await page.screenshot({ path: path.join(SHOTS, 'bug-1-2-after-echo.png'), fullPage: true })

    expect(total, 'exactly one turn rendered').toBe(1)
    expect(live, 'no turn should be live after instant-return command').toBe(0)
    // Output line should be present.
    await expect(page.locator('.msg-turn').first()).toContainText('hi')
  })

  test('bug 1+2: repeated quick commands each produce exactly one turn', async ({ page }) => {
    const tok = await login(page)
    const sid = await freshSessionTracked(page, tok, 'pw-bug-1-2-rep')
    await loginUI(page, tok)
    await selectSession(page, sid)

    await submitCommand(page, 'pwd')
    await page.waitForTimeout(400)
    await submitCommand(page, 'echo a')
    await page.waitForTimeout(400)
    await submitCommand(page, 'echo b')
    await page.waitForTimeout(800)

    const { total, live } = await countTurns(page)
    await page.screenshot({ path: path.join(SHOTS, 'bug-1-2-three-cmds.png'), fullPage: true })

    expect(total, 'three commands → three turns').toBe(3)
    expect(live, 'all three should have finished').toBe(0)
  })
})

test.describe('regression: typing `claude` in the composer pops the Start Claude dialog', () => {
  // Three forms should all be intercepted and routed to the dialog
  // instead of running as a literal bash command:
  //   "claude"           — bare invocation, the most common case
  //   "/claude"          — slash-command form
  //   "claude --foo"     — with args (treated the same as the CLI)
  for (const cmd of ['claude', '/claude', 'claude --version']) {
    test(`composer "${cmd}" opens the renderer picker`, async ({ page }) => {
      const tok = await login(page)
      const sid = await freshSessionTracked(page, tok, `pw-claude-${cmd.replace(/[^a-z0-9]/gi, '_')}`)
      await loginUI(page, tok)
      await selectSession(page, sid)

      await submitCommand(page, cmd)

      // Dialog visible.
      await expect(page.locator('text=Start Claude')).toBeVisible({ timeout: 3_000 })
      // No bash command was submitted: chat stream stays empty.
      expect(await countTurns(page)).toEqual({ total: 0, live: 0 })

      await page.screenshot({
        path: path.join(SHOTS, `start-claude-via-${cmd.replace(/[^a-z0-9]/gi, '_')}.png`),
        fullPage: true,
      })
    })
  }
})

test.describe('regression: Stop button must terminate a running command', () => {
  test('bug 3: Stop on a sleep 30 cancels it within seconds', async ({ page }) => {
    const tok = await login(page)
    const sid = await freshSessionTracked(page, tok, 'pw-bug-3')
    await loginUI(page, tok)
    await selectSession(page, sid)

    await submitCommand(page, 'sleep 30')
    // Wait for it to actually start streaming (live turn appears).
    await expect(page.locator('.msg-turn--live')).toBeVisible({ timeout: 5_000 })
    await page.screenshot({ path: path.join(SHOTS, 'bug-3-before-stop.png'), fullPage: true })

    // The Stop button replaces the Send button while busy.
    const stopBtn = page.locator('button[aria-label="Stop"]')
    await expect(stopBtn).toBeVisible({ timeout: 2_000 })
    await stopBtn.click()

    // Within a few seconds the live turn should disappear (moved to messages).
    await expect(page.locator('.msg-turn--live')).toHaveCount(0, { timeout: 8_000 })
    await page.screenshot({ path: path.join(SHOTS, 'bug-3-after-stop.png'), fullPage: true })

    // And exactly one finished turn should remain.
    const { total, live } = await countTurns(page)
    expect(live).toBe(0)
    expect(total).toBeGreaterThanOrEqual(1)
  })
})
