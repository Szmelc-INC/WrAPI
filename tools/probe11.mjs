import { chromium } from 'playwright';
import { execSync } from 'child_process';
import fs from 'fs';
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
const run = (c) => { try { return execSync(c + ' 2>/dev/null').toString().trim(); } catch { return ''; } };
let root = '';
for (const pid of fs.readdirSync('/proc').filter(p => /^\d+$/.test(p))) {
  try {
    const cmd = fs.readFileSync(`/proc/${pid}/cmdline`, 'utf8').replace(/\0/g, ' ');
    if (cmd.includes('WRAPI/data/profile') && cmd.includes('remote-debugging-pipe') && !cmd.includes('--type=')) { root = pid; break; }
  } catch {}
}
const ids = run(`xdotool search --pid ${root}`).split('\n').filter(Boolean);
console.log('windows:', ids);
for (const id of ids) console.log(id, run(`xdotool getwindowgeometry ${id}`));
// move last window to corner 3840-wide twin screen: bottom-right of DP-1
const id = ids[ids.length - 1];
run(`xdotool windowmove ${id} 1590 850`);
run(`xdotool windowsize ${id} 300 200`);
await page.waitForTimeout(800);
for (const w of ids) console.log('after move', w, run(`xdotool getwindowgeometry ${w}`));
// also move it offscreen again to verify that works
run(`xdotool windowmove ${id} -32000 -32000`);
await page.waitForTimeout(800);
console.log('after offscreen', run(`xdotool getwindowgeometry ${id}`));
await ctx.close();
