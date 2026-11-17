import { chromium } from 'playwright';
import { execSync, spawn } from 'child_process';
import fs from 'fs';
// spawn Xvfb :99
const xvfb = spawn('Xvfb', [':99', '-screen', '0', '1920x1080x24', '-nolisten', 'tcp'], { stdio: 'ignore' });
for (let i = 0; i < 30 && !fs.existsSync('/tmp/.X11-unix/X99'); i++) await new Promise(r => setTimeout(r, 200));
console.log('Xvfb socket:', fs.existsSync('/tmp/.X11-unix/X99'));
process.env.DISPLAY = ':99';
const profile = '/home/silverx/Projects-5/WRAPI/data/profile';
const ctx = await chromium.launchPersistentContext(profile, {
  channel: 'chromium', headless: false, locale: 'en-US',
  args: ['--disable-blink-features=AutomationControlled', '--no-first-run',
         '--window-size=1440,900', '--window-position=0,0'],
  ignoreDefaultArgs: ['--enable-automation'],
});
const page = ctx.pages()[0] || await ctx.newPage();
await page.goto('https://www.google.com/search?q=What+is+the+capital+of+Poland%3F&udm=50&csuir=1&hl=en', { waitUntil: 'domcontentloaded', timeout: 45000 });
await page.waitForSelector('div[data-subtree="aimc"]', { timeout: 30000 });
await page.waitForTimeout(4000);
const len = await page.evaluate(`(() => { const p = document.querySelectorAll('div[data-subtree="aimc"]'); const a = p[p.length-1]; return a ? a.innerText.length : 0; })()`);
console.log('Xvfb headed chromium: aimc len =', len);
await ctx.close();
xvfb.kill();
