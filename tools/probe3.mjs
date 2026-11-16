import { chromium } from 'playwright';

const profile = '/home/silverx/Projects-5/WRAPI/data/profile';
const ctx = await chromium.launchPersistentContext(profile, {
  channel: 'chromium', headless: process.env.PROBE_HEADLESS !== '0', locale: 'en-US',
  args: ['--disable-blink-features=AutomationControlled', '--no-first-run'],
  ignoreDefaultArgs: ['--enable-automation'],
});
const page = ctx.pages()[0] || await ctx.newPage();
await page.goto('https://www.google.com/search?q=What+is+the+capital+of+Poland%3F+and+what+is+its+population&udm=50&csuir=1&hl=en', { waitUntil: 'domcontentloaded', timeout: 45000 });
await page.waitForSelector('div[data-subtree="aimc"]', { timeout: 30000 });
await page.waitForFunction(() => {
  const el = document.querySelector('div[data-subtree="aimc"]');
  return el && el.innerText.length > 100;
}, { timeout: 30000 });
await page.waitForTimeout(5000);

const info = await page.evaluate(() => {
  const aimc = document.querySelector('div[data-subtree="aimc"]');
  const res = { aimcTextLen: aimc.innerText.length, links: [], citations: [], buttons: [] };
  aimc.querySelectorAll('a').forEach(a => {
    res.links.push({ href: (a.getAttribute('href') || '').slice(0, 100), text: a.innerText.trim().slice(0, 50), cls: (a.className + '').slice(0, 60) });
  });
  // citation-ish elements
  aimc.querySelectorAll('[data-rl],[data-fc-id],cite,.Y0f3ac,.TPp9Pc,[aria-label*="source" i],[aria-label*="citation" i]').forEach(el => {
    res.citations.push({ tag: el.tagName, cls: (el.className + '').slice(0, 70), aria: el.getAttribute('aria-label'), href: (el.getAttribute('href') || '').slice(0, 80) });
  });
  // shadow roots?
  res.shadowHosts = [...aimc.querySelectorAll('*')].filter(el => el.shadowRoot).length;
  res.answerText = aimc.innerText.slice(0, 1500);
  return res;
});
console.log(JSON.stringify(info, null, 2));
await ctx.close();
