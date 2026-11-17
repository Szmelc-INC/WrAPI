import { chromium } from 'playwright';
import { execSync } from 'child_process';
import fs from 'fs';
const profile = '/home/silverx/Projects-5/WRAPI/data/profile';
const ctx = await chromium.launchPersistentContext(profile, {
  channel: 'chromium', headless: false, locale: 'en-US',
  args: ['--disable-blink-features=AutomationControlled', '--no-first-run',
         '--ozone-platform=x11', '--window-size=300,200'],
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
const id = ids.sort((a,b)=>Number(b)-Number(a))[1] || ids[0]; // main window heuristic
for (const w of ids) console.log(w, run(`xdotool getwindowgeometry ${w}`).replace(/\n/g,' '));
const main = ids.find(i => run(`xdotool getwindowgeometry ${i}`).includes('843x1040')) || id;
run(`xdotool windowsize ${main} 300 200`);
await page.waitForTimeout(1000);
console.log('after resize 300x200:', run(`xdotool getwindowgeometry ${main}`).replace(/\n/g,' '));
run(`xdotool windowsize ${main} 120 90`);
await page.waitForTimeout(1000);
console.log('after resize 120x90:', run(`xdotool getwindowgeometry ${main}`).replace(/\n/g,' '));
run(`xdotool windowmove ${main} 100 100`);
await page.waitForTimeout(800);
console.log('after move 100,100:', run(`xdotool getwindowgeometry ${main}`).replace(/\n/g,' '));
await ctx.close();
