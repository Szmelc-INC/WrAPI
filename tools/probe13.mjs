import { chromium } from 'playwright';
import { execSync } from 'child_process';
import fs from 'fs';
const profile = '/home/silverx/Projects-5/WRAPI/data/profile';
const run = (c) => { try { return execSync(c + ' 2>/dev/null').toString().trim(); } catch { return ''; } };
const findRoot = () => {
  for (const pid of fs.readdirSync('/proc').filter(p => /^\d+$/.test(p))) {
    try {
      const cmd = fs.readFileSync(`/proc/${pid}/cmdline`, 'utf8').replace(/\0/g, ' ');
      if (cmd.includes('WRAPI/data/profile') && cmd.includes('remote-debugging-pipe') && !cmd.includes('--type=')) return pid;
    } catch {}
  }
  return '';
};
const geom = (root) => run(`xdotool search --pid ${root}`).split('\n').filter(Boolean)
  .map(i => run(`xdotool getwindowgeometry --shell ${i}`).replace(/\n/g, ' '))
  .join(' || ');

// case A: viewport null + offscreen position + big window
let ctx = await chromium.launchPersistentContext(profile, {
  channel: 'chromium', headless: false, locale: 'en-US', viewport: null,
  args: ['--disable-blink-features=AutomationControlled', '--no-first-run',
         '--ozone-platform=x11', '--window-size=1400,900', '--window-position=-32000,-32000'],
  ignoreDefaultArgs: ['--enable-automation'],
});
let page = ctx.pages()[0] || await ctx.newPage();
await page.goto('about:blank'); await page.waitForTimeout(1500);
console.log('A viewport:null offscreen:', geom(findRoot()));
await ctx.close();
await new Promise(r => setTimeout(r, 2000));

// case B: viewport null + corner position + small window
ctx = await chromium.launchPersistentContext(profile, {
  channel: 'chromium', headless: false, locale: 'en-US', viewport: null,
  args: ['--disable-blink-features=AutomationControlled', '--no-first-run',
         '--ozone-platform=x11', '--window-size=300,200', '--window-position=1580,840'],
  ignoreDefaultArgs: ['--enable-automation'],
});
page = ctx.pages()[0] || await ctx.newPage();
await page.goto('about:blank'); await page.waitForTimeout(1500);
console.log('B viewport:null corner small:', geom(findRoot()));
const inner = await page.evaluate('({w: window.innerWidth, h: window.innerHeight})');
console.log('B inner size:', JSON.stringify(inner));
await ctx.close();
