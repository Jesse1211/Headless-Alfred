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
    data: { user: ALFRED_USER, password: ALFRED_PASSWORD },
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

test('monitor_task_lifecycle_completes', async ({ page }) => {
  test.setTimeout(180_000)  // 3 min wall clock
  const token = await login(page)
  const sid = await freshSession(page, token, 'monitor-life')
  await loginUI(page, token)
  await page.locator(`[data-session-id="${sid}"], li:has-text("pw-v04-monitor-life")`).first().click()
  await enterClaudeUI(page)

  // Ask Claude to dispatch a Monitor with a known short polling
  // command. The polling emits one line every 4s for 12s total.
  await sendPrompt(page,
    'Use the Monitor tool to run this exact bash: `for i in 1 2 3; do echo poll-$i; sleep 4; done`. ' +
    'Description should be "test poll 12s". Just dispatch and respond "monitor dispatched".'
  )

  // Wait for the Monitor card to appear with a task id.
  const monitorCard = page.locator('.claude-tool:has(.claude-tool__name:text-is("Monitor"))').last()
  await expect(monitorCard).toBeVisible({ timeout: 60_000 })
  await expect(monitorCard.locator('.claude-tool__bg code')).toBeVisible({ timeout: 60_000 })
  const taskIdText = await monitorCard.locator('.claude-tool__bg code').first().textContent()
  expect(taskIdText).toBeTruthy()
  expect(taskIdText!.length).toBeGreaterThan(3)

  // While the task runs, the notification-count number should rise above 0.
  await expect(monitorCard.locator('.claude-tool__bg')).toContainText(/\d+ events/, { timeout: 30_000 })

  // Eventually the check mark appears once task_updated.status=completed lands.
  await expect(monitorCard.locator('.claude-tool__check')).toBeVisible({ timeout: 60_000 })
})

test('multiple_concurrent_monitors', async ({ page }) => {
  test.setTimeout(240_000)
  const token = await login(page)
  const sid = await freshSession(page, token, 'concurrent-mon')
  await loginUI(page, token)
  await page.locator(`li:has-text("pw-v04-concurrent-mon")`).first().click()
  await enterClaudeUI(page)

  await sendPrompt(page,
    'Dispatch three Monitor tools in parallel, each running a different 12-second bash command. ' +
    'First: `for i in 1 2 3; do echo a-$i; sleep 4; done` (description: "stream A"). ' +
    'Second: `for i in 1 2 3; do echo b-$i; sleep 4; done` (description: "stream B"). ' +
    'Third: `for i in 1 2 3; do echo c-$i; sleep 4; done` (description: "stream C"). ' +
    'Then respond "all three dispatched".'
  )

  // Wait until three Monitor cards are visible.
  const monitorCards = page.locator('.claude-tool:has(.claude-tool__name:text-is("Monitor"))')
  await expect(monitorCards).toHaveCount(3, { timeout: 90_000 })

  // Sidebar pill should show ≥3 active tasks while they're running.
  // Tooltip enumerates monitor task count.
  const pill = page.locator('.session-pill').first()
  await expect(pill).toBeVisible({ timeout: 30_000 })
  await expect(pill).toContainText(/[3-9]/)

  // All three eventually checkmark.
  await expect(monitorCards.locator('.claude-tool__check')).toHaveCount(3, { timeout: 120_000 })
})

test('subagent_long_running', async ({ page }) => {
  test.setTimeout(360_000)  // 6 min
  const token = await login(page)
  const sid = await freshSession(page, token, 'subagent-long')
  await loginUI(page, token)
  await page.locator(`li:has-text("pw-v04-subagent-long")`).first().click()
  await enterClaudeUI(page)

  await sendPrompt(page,
    'Use the Task / Agent tool to dispatch a general-purpose subagent with this task: ' +
    '"List the 10 largest files under /tmp by size. Use ls + sort. ' +
    'Take your time, walk through it carefully step by step." ' +
    'Then wait for the subagent to finish and report back its summary.'
  )

  // Agent card appears.
  const agentCard = page.locator('.claude-tool:has(.claude-tool__name:text-is("Task"), .claude-tool__name:text-is("Agent"))').last()
  await expect(agentCard).toBeVisible({ timeout: 60_000 })

  // Elapsed timer should be ticking (>1s) while running.
  const elapsed = agentCard.locator('.claude-tool__elapsed').first()
  await expect(elapsed).toBeVisible({ timeout: 30_000 })
  const t0 = await elapsed.textContent()
  await page.waitForTimeout(3_000)
  const t1 = await elapsed.textContent()
  expect(t1).not.toBe(t0)

  // Eventually the agent finishes — turn becomes done, phase chip says "Done".
  await expect(page.locator('.turn-phase-chip--done').last()).toBeVisible({ timeout: 240_000 })

  // Turn stats line mentions at least 1 subagent.
  await expect(page.locator('.turn-stats').last()).toContainText(/subagent/)
})

test('stats_line_shows_zero_when_claude_only_says_it_will_monitor', async ({ page }) => {
  test.setTimeout(120_000)
  const token = await login(page)
  const sid = await freshSession(page, token, 'promise-mismatch')
  await loginUI(page, token)
  await page.locator(`li:has-text("pw-v04-promise-mismatch")`).first().click()
  await enterClaudeUI(page)

  // Prompt that explicitly tells Claude NOT to dispatch any tool.
  await sendPrompt(page,
    'Just respond with the exact text: ' +
    '"OK, I will monitor CI and let you know when it finishes." ' +
    'Do not use any tools. Reply with that one sentence only.'
  )

  // Wait for turn to complete.
  await expect(page.locator('.turn-phase-chip--done').last()).toBeVisible({ timeout: 60_000 })
  // Stats line for this turn should say zero tool calls — no Monitor.
  const stats = page.locator('.turn-stats').last()
  await expect(stats).toContainText(/0 tool calls/)
  await expect(stats).not.toContainText(/Monitor/)
})

test('stats_line_shows_count_when_monitor_actually_dispatched', async ({ page }) => {
  test.setTimeout(120_000)
  const token = await login(page)
  const sid = await freshSession(page, token, 'promise-truthful')
  await loginUI(page, token)
  await page.locator(`li:has-text("pw-v04-promise-truthful")`).first().click()
  await enterClaudeUI(page)

  await sendPrompt(page,
    'Use the Monitor tool to run `echo hi; sleep 2; echo done`. Description: "quick poll". Just dispatch.'
  )

  await expect(page.locator('.turn-phase-chip--done').last()).toBeVisible({ timeout: 60_000 })
  await expect(page.locator('.turn-stats').last()).toContainText(/1 Monitor task/)
})

test('monitor_completes_after_parent_result', async ({ page }) => {
  test.setTimeout(240_000)
  const token = await login(page)
  const sid = await freshSession(page, token, 'after-result')
  await loginUI(page, token)
  await page.locator(`li:has-text("pw-v04-after-result")`).first().click()
  await enterClaudeUI(page)

  await sendPrompt(page,
    'Dispatch one Monitor tool to run `for i in 1 2 3 4 5; do echo step-$i; sleep 6; done`. ' +
    'Description: "30s monitor". Just dispatch and immediately reply "monitor running".'
  )

  // The PARENT turn should finish quickly (phase chip "Done") because
  // Monitor is detached. The Monitor card should still be ticking.
  await expect(page.locator('.turn-phase-chip--done').last()).toBeVisible({ timeout: 60_000 })

  const monitorCard = page.locator('.claude-tool:has(.claude-tool__name:text-is("Monitor"))').last()
  await expect(monitorCard).toBeVisible()

  // After the parent turn done, the Monitor card has NO checkmark yet.
  await expect(monitorCard.locator('.claude-tool__check')).toHaveCount(0)

  // The sidebar pill is still active.
  await expect(page.locator('.session-pill').first()).toBeVisible()

  // Wait for the eventual task_updated.completed → checkmark.
  await expect(monitorCard.locator('.claude-tool__check')).toBeVisible({ timeout: 90_000 })

  // Pill disappears once the Monitor is done.
  await expect(page.locator('.session-pill')).toHaveCount(0, { timeout: 30_000 })
})
