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
// First snapshot — BEFORE any scroll
const before = await page.evaluate(() => {
  // Find ANY element whose bounding rect extends past the viewport,
  // anywhere in the DOM.
  const overflowing = []
  document.querySelectorAll('*').forEach((e) => {
    const r = e.getBoundingClientRect()
    if (r.bottom > 1000 || r.right > 1500) {
      overflowing.push({
        tag: e.tagName,
        cls: typeof e.className === 'string' ? e.className : '?',
        top: Math.round(r.top),
        left: Math.round(r.left),
        bottom: Math.round(r.bottom),
        right: Math.round(r.right),
        h: Math.round(r.height),
        w: Math.round(r.width),
      })
    }
  })
  return {
    htmlScrollH: document.documentElement.scrollHeight,
    overflowing: overflowing.slice(0, 20),
  }
})
console.error('BEFORE-SCROLL:', JSON.stringify(before))
// Scroll the chat scroll area to the bottom (where the user says the bug shows up).
await page.evaluate(() => {
  const s = document.querySelector('.claude-chat__scroll')
  if (s) s.scrollTop = s.scrollHeight
})
await page.waitForTimeout(400)
// Also scroll the WINDOW down in case page itself overflows.
await page.evaluate(() => window.scrollTo(0, document.body.scrollHeight))
await page.waitForTimeout(200)
// And screenshot for visual confirmation
await page.screenshot({ path: '/tmp/alfred-shot.png', fullPage: false })

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
      scrollH: el.scrollHeight,
      display: cs.display,
      gridRows: cs.gridTemplateRows,
      minHeight: cs.minHeight,
      overflow: cs.overflow,
      heightProp: cs.height,
    }
  }
  // Also probe the immediate children of claude-chat to see what
  // is actually in there — maybe an extra wrapper crept in.
  const chat = document.querySelector('.claude-chat')
  const chatChildren = chat ? Array.from(chat.children).map((c) => ({
    tag: c.tagName,
    cls: c.className,
    h: Math.round(c.getBoundingClientRect().height),
    scrollH: c.scrollHeight,
  })) : []
  return {
    viewport: { h: window.innerHeight, w: window.innerWidth },
    bodyScrollH: document.body.scrollHeight,
    cssLink: document.querySelector('link[rel=stylesheet]')?.href,
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
    chatChildren,
    htmlNonStandard: Array.from(document.documentElement.querySelectorAll('*')).filter((e) => {
      const tag = e.tagName.toLowerCase()
      return tag.includes('-') || tag === 'iframe'
    }).slice(0, 10).map((e) => {
      const r = e.getBoundingClientRect()
      return { tag: e.tagName, h: Math.round(r.height), top: Math.round(r.top), bottom: Math.round(r.bottom) }
    }),
    htmlChildren: Array.from(document.documentElement.children).map((c) => {
      const r = c.getBoundingClientRect()
      return {
        tag: c.tagName,
        id: c.id || null,
        cls: typeof c.className === 'string' ? c.className : '(non-string)',
        top: Math.round(r.top),
        h: Math.round(r.height),
        scrollH: c.scrollHeight,
      }
    }),
    bodyChildren: Array.from(document.body.children).map((c) => {
      const r = c.getBoundingClientRect()
      return {
        tag: c.tagName,
        id: c.id || null,
        cls: typeof c.className === 'string' ? c.className : '(non-string)',
        top: Math.round(r.top),
        bottom: Math.round(r.bottom),
        h: Math.round(r.height),
      }
    }),
  }
})

console.log(JSON.stringify(result, null, 2))
await browser.close()
