import { chromium } from 'playwright';
import fs from 'fs';

const profile = '/home/silverx/Projects-5/WRAPI/data/profile';
const ctx = await chromium.launchPersistentContext(profile, {
  channel: 'chromium',
  headless: process.env.PROBE_HEADLESS !== '0',
  locale: 'en-US',
  args: ['--disable-blink-features=AutomationControlled', '--no-first-run', '--no-default-browser-check'],
  ignoreDefaultArgs: ['--enable-automation'],
});
const page = ctx.pages()[0] || await ctx.newPage();
await page.goto('https://www.google.com/search?q=What+is+the+capital+of+Poland%3F&udm=50&csuir=1&hl=en', { waitUntil: 'domcontentloaded', timeout: 45000 });
try {
  await page.waitForSelector('text=Warsaw', { timeout: 30000 });
} catch { console.log('no Warsaw text yet'); }
await page.waitForTimeout(4000);

const info = await page.evaluate(() => {
  const out = { answer: null, sources: [] };
  // find element directly containing "Warsaw"
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  let node;
  while ((node = walker.nextNode())) {
    if (node.textContent.includes('Warsaw')) {
      let el = node.parentElement;
      const chain = [];
      for (let i = 0; el && i < 12; i++, el = el.parentElement) {
        chain.push(`${el.tagName}#${el.id || '-'} cls="${(el.className + '').slice(0, 120)}" dataattrs=${JSON.stringify(Object.fromEntries([...el.attributes].filter(a => a.name.startsWith('data')).map(a => [a.name, a.value.slice(0, 30)])))}`);
      }
      out.answer = chain;
      break;
    }
  }
  // candidate source links: external http links in page
  const links = [...document.querySelectorAll('a[href^="http"]')].filter(a => {
    const h = a.href;
    return !h.includes('google.') && !h.includes('gstatic') && a.offsetParent !== null;
  });
  out.sources = links.slice(0, 25).map(a => ({
    href: a.href.slice(0, 120),
    text: (a.textContent || '').trim().slice(0, 60),
    cls: (a.className + '').slice(0, 80),
    parentCls: (a.parentElement.className + '').slice(0, 80),
  }));
  out.count = links.length;
  return out;
});
console.log(JSON.stringify(info, null, 2));
fs.writeFileSync('/home/silverx/Projects-5/WRAPI/data/probe/answer.json', JSON.stringify(info, null, 2));
await ctx.close();
