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
await page.waitForTimeout(4000);

const sel = '[data-xid="aim-mars-input-plate"] textarea, textarea[aria-label="Ask anything"]';
const input = page.locator(sel).first();
await input.click();
await input.fill('What is the tallest building in Warsaw?');
await page.waitForTimeout(500);
const sendEnabled = await page.evaluate(() => {
  const b = document.querySelector('[data-xid="input-plate-send-button"]');
  return b ? !b.disabled : null;
});
console.log('send button enabled:', sendEnabled);
await page.keyboard.press('Enter');
await page.waitForTimeout(6000);
console.log('URL after enter:', page.url().slice(0, 110));
const turns = await page.evaluate(() => [...document.querySelectorAll('[data-scope-id="turn"]')].length);
console.log('turn count:', turns);
const text = await page.evaluate(() => document.querySelector('div[data-subtree="aimc"]').innerText.slice(0, 300));
console.log('TEXT:', text);
await ctx.close();
