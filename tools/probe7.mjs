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
await page.waitForTimeout(5000);

const sel = '[data-xid="aim-mars-input-plate"] textarea, textarea[aria-label="Ask anything"]';
const comp = page.locator(sel).first();
console.log('composer count:', await page.locator(sel).count());
await comp.click();
await comp.fill('What is the tallest building in Warsaw?');
await page.waitForTimeout(400);
console.log('value after fill:', JSON.stringify(await comp.inputValue()));
console.log('turns before:', await page.evaluate(`document.querySelectorAll('div[data-subtree="aimc"] [data-scope-id="turn"]').length`));
await comp.press('Enter');
await page.waitForTimeout(5000);
console.log('turns after:', await page.evaluate(`document.querySelectorAll('div[data-subtree="aimc"] [data-scope-id="turn"]').length`));
console.log('value after enter:', JSON.stringify(await comp.inputValue()));
console.log('url:', page.url().slice(0, 100));
const last = await page.evaluate(() => {
  const t = document.querySelectorAll('[data-scope-id="turn"]');
  return t[t.length-1]?.innerText.slice(0, 200);
});
console.log('LAST TURN:', last);
await ctx.close();
