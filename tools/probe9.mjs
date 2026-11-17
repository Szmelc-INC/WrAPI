import { chromium } from 'playwright';
const profile = '/home/silverx/Projects-5/WRAPI/data/profile';
const ctx = await chromium.launchPersistentContext(profile, {
  channel: 'chromium', headless: false, locale: 'en-US',
  args: ['--disable-blink-features=AutomationControlled', '--no-first-run',
         '--ozone-platform=x11', '--window-size=300,200', '--window-position=-32000,-32000'],
  ignoreDefaultArgs: ['--enable-automation'],
});
const page = ctx.pages()[0] || await ctx.newPage();
await page.goto('about:blank');
await page.waitForTimeout(1500);
const cdp = await ctx.newCDPSession(page);
const q = async (label) => {
  const w = await cdp.send('Browser.getWindowForTarget');
  console.log(label, JSON.stringify(w.bounds));
};
await q('launched offscreen:');
await page.evaluate('({w: screen.width, h: screen.height, aw: screen.availWidth, ah: screen.availHeight})').then(s => console.log('page screen obj:', s));
// try tiny sizes via CDP
for (const b of [{ left: 1200, top: 600, width: 300, height: 200 }, { left: 1200, top: 600, width: 100, height: 100 }, { left: 1200, top: 600, width: 10, height: 10 }]) {
  await cdp.send('Browser.setWindowBounds', { windowId: (await cdp.send('Browser.getWindowForTarget')).windowId, bounds: b });
  await q(`set ${JSON.stringify(b)} ->`);
}
await ctx.close();
