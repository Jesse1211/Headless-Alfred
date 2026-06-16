import { chromium } from 'playwright'

const SESSION_ID = '01KV797YF69BDQH5YK3YEXT27Z' // Session 8

const browser = await chromium.launch()
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
const page = await ctx.newPage()

// Pre-seed auth token in localStorage so we skip the login UI.
await page.goto('http://localhost:5173/')
await page.evaluate(() => {
  localStorage.setItem('alfred_token', 'devtoken')
})
await page.reload()
await page.waitForSelector('.workspace', { timeout: 5000 })
// Click the session row to make sure it's selected
await page.evaluate((sid) => {
  const rows = document.querySelectorAll('[data-testid="session-row"]')
  for (const r of rows) {
    if (r.textContent?.includes('Session 8')) { r.click(); break }
  }
}, SESSION_ID)
await page.waitForTimeout(2500)

// Walk the layout chain
const result = await page.evaluate(() => {
  function info(sel) {
    const el = document.querySelector(sel)
    if (!el) return { sel, missing: true }
    const r = el.getBoundingClientRect()
    const cs = getComputedStyle(el)
    return {
      sel,
      h: Math.round(r.height),
      w: Math.round(r.width),
      display: cs.display,
      gridRows: cs.gridTemplateRows,
      minHeight: cs.minHeight,
      overflow: cs.overflow,
      heightProp: cs.height,
    }
  }
  return {
    viewport: { h: window.innerHeight, w: window.innerWidth },
    bodyScrollH: document.body.scrollHeight,
    chain: [
      info('html'),
      info('body'),
      info('#root'),
      info('.workspace'),
      info('.workspace__main'),
      info('.workspace__header'),
      info('.claude-chat'),
      info('.claude-chat__scroll'),
      info('.claude-chat__composer'),
    ],
  }
})

console.log(JSON.stringify(result, null, 2))
await browser.close()
