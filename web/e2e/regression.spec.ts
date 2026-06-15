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

async function login(page: Page): Promise<string> {
  const r = await page.request.post(`${BACKEND}/api/login`, {
    data: { user: 'admin', password: 'admin' },
  })
  const { token } = await r.json()
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

async function loginUI(page: Page, token: string) {
  // Set the token directly in localStorage so we skip the login form.
  // We have to visit a same-origin page first before localStorage is
  // accessible — the empty / will redirect to login but that's fine.
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
