import { chromium } from 'playwright';
const profile = '/home/silverx/Projects-5/WRAPI/data/profile';
const ctx = await chromium.launchPersistentContext(profile, {
  channel: 'chromium', headless: false, locale: 'en-US',
  args: ['--disable-blink-features=AutomationControlled', '--no-first-run',
         '--ozone-platform=x11', '--window-size=320,240', '--window-position=0,0'],
  ignoreDefaultArgs: ['--enable-automation'],
});
const page = ctx.pages()[0] || await ctx.newPage();
await page.goto('https://www.google.com/search?q=What+is+the+capital+of+Poland%3F&udm=50&csuir=1&hl=en', { waitUntil: 'domcontentloaded', timeout: 45000 });
await page.waitForSelector('div[data-subtree="aimc"]', { timeout: 30000 });
await page.waitForTimeout(4000);
const len = await page.evaluate(`(() => { const p = document.querySelectorAll('div[data-subtree="aimc"]'); const a = p[p.length-1]; return a ? a.innerText.length : 0; })()`);
console.log('X11-ozone mini window: aimc text len =', len);
const screen = await page.evaluate('({w: screen.width, h: screen.height})');
console.log('screen:', screen);
const cdp = await ctx.newCDPSession(page);
const win = await cdp.send('Browser.getWindowForTarget');
console.log('window bounds before:', win.bounds);
const W = 320, H = 240;
await cdp.send('Browser.setWindowBounds', { windowId: win.windowId, bounds: { windowState: 'normal' } });
const res = await cdp.send('Browser.setWindowBounds', { windowId: win.windowId, bounds: { left: screen.w - W - 10, top: screen.h - H - 60, width: W, height: H, windowState: 'normal' } });
const win2 = await cdp.send('Browser.getWindowForTarget');
console.log('bounds after corner move:', win2.bounds, 'err:', res);
// minimized state test
await cdp.send('Browser.setWindowBounds', { windowId: win.windowId, bounds: { windowState: 'minimized' } });
const win3 = await cdp.send('Browser.getWindowForTarget');
console.log('bounds after minimize:', win3.bounds);
await ctx.close();
