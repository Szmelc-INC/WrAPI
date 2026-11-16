import { chromium } from 'playwright';
const profile = '/home/silverx/Projects-5/WRAPI/data/profile';
const ctx = await chromium.launchPersistentContext(profile, {
  channel: 'chromium', headless: false, locale: 'en-US',
  args: ['--disable-blink-features=AutomationControlled', '--no-first-run', '--window-position=-32000,-32000'],
  ignoreDefaultArgs: ['--enable-automation'],
});
const page = ctx.pages()[0] || await ctx.newPage();
await page.goto('https://www.google.com/search?q=What+is+the+capital+of+Poland%3F&udm=50&csuir=1&hl=en', { waitUntil: 'domcontentloaded', timeout: 45000 });
await page.waitForSelector('div[data-subtree="aimc"]', { timeout: 30000 });
await page.waitForTimeout(6000);
const info = await page.evaluate(() => {
  const els = [...document.querySelectorAll('textarea, [contenteditable="true"], [role="textbox"]')];
  return els.map(el => {
    const r = el.getBoundingClientRect();
    const cs = getComputedStyle(el);
    return {
      tag: el.tagName, cls: (el.className + '').slice(0, 60),
      aria: el.getAttribute('aria-label'), placeholder: el.getAttribute('placeholder'),
      xid: el.closest('[data-xid]')?.getAttribute('data-xid'),
      vis: r.width > 0 && r.height > 0 && cs.visibility !== 'hidden' && cs.display !== 'none',
      rect: [Math.round(r.x), Math.round(r.y), Math.round(r.width), Math.round(r.height)],
    };
  });
});
console.log(JSON.stringify(info, null, 2));
await ctx.close();
