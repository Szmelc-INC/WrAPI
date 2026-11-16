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
await page.waitForFunction(() => document.querySelector('div[data-subtree="aimc"]').innerText.length > 300, { timeout: 30000 });
await page.waitForTimeout(5000);
const info = await page.evaluate(() => {
  const aimc = document.querySelector('div[data-subtree="aimc"]');
  const cards = [];
  aimc.querySelectorAll('a.vIWmYe').forEach(a => {
    const chain = [];
    let el = a.parentElement;
    for (let i = 0; el && i < 5; i++, el = el.parentElement) {
      chain.push(`${el.tagName}.${(el.className+'').split(' ').slice(0,3).join('.')}`);
    }
    cards.push({ chain, parentText: a.parentElement.innerText.slice(0, 200) });
  });
  // main-col vs full aimc text
  const mainCol = aimc.querySelector('[data-container-id="main-col"]');
  return { cardCount: cards.length, cards: cards.slice(0, 3),
    mainColLen: mainCol ? mainCol.innerText.length : 0,
    mainColTail: mainCol ? mainCol.innerText.slice(-200) : null,
    aimcTail: aimc.innerText.slice(-300) };
});
console.log(JSON.stringify(info, null, 2));
await ctx.close();
