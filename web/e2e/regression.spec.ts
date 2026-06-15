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

test.describe('regression: Claude TUI mode renderer lifecycle', () => {
  // Bug B: switching to another session and back used to crash xterm
  //   ("Cannot read properties of undefined (reading 'dimensions')"),
  //   wiping the main area to blank. The dispose+re-mount race against
  //   xterm.js' queued viewport refresh now defers dispose by one tick.
  test('switching session away and back keeps the workspace rendered', async ({ page }) => {
    const pageErrors: string[] = []
    page.on('pageerror', (e) => pageErrors.push(e.message))

    const tok = await login(page)
    const sidTUI = await freshSessionTracked(page, tok, 'pw-tui-switch-a')
    const sidShell = await freshSessionTracked(page, tok, 'pw-tui-switch-b')

    await loginUI(page, tok)
    await selectSession(page, sidTUI)

    // Enter Claude TUI.
    await page.locator('.workspace__claude-btn').click()
    await expect(page.locator('text=Start Claude')).toBeVisible()
    await page.locator('label:has-text("Terminal")').click()
    await page.locator('button:has-text("Start")').click()
    await page.waitForTimeout(2000)

    // Switch to the shell session…
    await page.locator(`text=pw-tui-switch-b`).click()
    await page.waitForTimeout(500)
    // …and back.
    await page.locator(`text=pw-tui-switch-a`).click()
    await page.waitForTimeout(800)

    // The Sign out button is a stable always-on element in the header;
    // if React's render aborted halfway it would not be present.
    await expect(page.locator('button:has-text("Sign out")')).toBeVisible()
    expect(pageErrors, 'no uncaught render errors during switch').toEqual([])
  })

})

test.describe('Claude UI: AskUserQuestion tool adaptation', () => {
  test('synthetic AskUserQuestion → card with options + submit + answer round-trip', async ({ page, request }) => {
    // We bypass the LLM entirely and POST a synthetic tool_approval
    // request directly to the bridge. Reasons:
    //   - The LLM is non-deterministic; under superpowers it
    //     intercepts AskUserQuestion and runs Skill first.
    //   - This isolates the front-end rendering path: we control
    //     the question shape, count clicks, and verify the answer
    //     comes back through the bridge as the tool_result.
    test.setTimeout(60_000)

    const tok = await login(page)
    const sid = await freshSessionTracked(page, tok, 'pw-ask-question')
    await loginUI(page, tok)
    await selectSession(page, sid)

    // Enter Claude UI mode and send one trivial prompt to allocate a
    // claude_session_id (needed for the bridge to route to us).
    await page.locator('.workspace__claude-btn').click()
    await expect(page.locator('text=Start Claude')).toBeVisible()
    await page.locator('label:has-text("Chat UI")').click()
    await page.locator('button:has-text("Start")').click()
    await expect(page.locator('textarea.claude-chat__input')).toBeVisible({ timeout: 5_000 })
    await page.locator('textarea.claude-chat__input').fill('reply with "k"')
    await page.locator('textarea.claude-chat__input').press('Enter')
    // Wait for the first turn to finish (composer goes back to idle).
    await expect(page.locator('textarea.claude-chat__input')).toHaveAttribute('placeholder', 'Message Claude…', { timeout: 45_000 })

    // Fetch the allocated claude_session_id from the API.
    const sessions = await request.get(`${BACKEND}/api/sessions`, {
      headers: { Authorization: `Bearer ${tok}` },
    }).then(r => r.json()) as Array<{ id: string; claude_session_id?: string }>
    const me = sessions.find(s => s.id === sid)
    expect(me?.claude_session_id, 'session must have a claude_session_id allocated').toBeTruthy()
    const convoID = me!.claude_session_id!

    // POST a synthetic AskUserQuestion hook request to the bridge.
    // The bridge will block on this until the frontend resolves it
    // via tool_decision. We fire and don't await.
    const toolUseId = `synth_${Date.now()}`
    const bridgePromise = request.post('http://127.0.0.1:8090/tool-approval', {
      data: {
        session_id: convoID,
        tool_use_id: toolUseId,
        hook_event_name: 'PreToolUse',
        tool_name: 'AskUserQuestion',
        tool_input: {
          questions: [{
            question: 'What is your favorite color?',
            header: 'Color',
            multiSelect: false,
            options: [
              { label: 'Red', description: 'Warm and bold' },
              { label: 'Blue', description: 'Cool and calm' },
              { label: 'Green', description: 'Natural and balanced' },
            ],
          }],
        },
      },
    })

    // The card should appear in the chat view.
    const card = page.locator('.ask-question')
    await expect(card).toBeVisible({ timeout: 5_000 })
    await expect(card).toContainText('Claude has a question')
    await expect(card).toContainText('What is your favorite color?')
    await expect(card.locator('label:has-text("Red")')).toBeVisible()
    await expect(card.locator('label:has-text("Blue")')).toBeVisible()
    await expect(card.locator('label:has-text("Green")')).toBeVisible()
    await page.screenshot({ path: path.join(SHOTS, 'ask-user-question-card.png'), fullPage: true })

    // Click "Blue" then Submit.
    await card.locator('label:has-text("Blue")').first().click()
    await page.locator('.ask-question__btn--submit').click()

    // Card disappears.
    await expect(card).toHaveCount(0, { timeout: 3_000 })

    // The bridge's POST returns the decision JSON Claude expects.
    // Nested under hookSpecificOutput per the PreToolUse contract.
    const bridgeResp = await bridgePromise
    expect(bridgeResp.status()).toBe(200)
    const decision = (await bridgeResp.json()).hookSpecificOutput
    expect(decision.permissionDecision).toBe('deny')
    expect(decision.permissionDecisionReason).toContain('Blue')
  })
})
