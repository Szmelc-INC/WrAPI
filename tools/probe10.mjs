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
// find root chromium pid with our user-data-dir
let root = '';
for (const pid of fs.readdirSync('/proc').filter(p => /^\d+$/.test(p))) {
  try {
    const cmd = fs.readFileSync(`/proc/${pid}/cmdline`, 'utf8').replace(/\0/g, ' ');
    if (cmd.includes('WRAPI/data/profile') && cmd.includes('remote-debugging-pipe') && !cmd.includes('--type=')) { root = pid; break; }
  } catch {}
}
console.log('root pid:', root);
for (const q of [`xdotool search --pid ${root}`, `xdotool search --onlyvisible --class ".*[Cc]hrom.*"`, `xdotool search --name "Chromium"`]) {
  console.log(q, '->', JSON.stringify(run(q)));
}
// X list all windows with names
console.log(run(`xdotool search --onlyvisible getwindowname %@ | head -20`).slice(0, 800));
await ctx.close();
