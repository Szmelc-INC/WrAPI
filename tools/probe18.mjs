import { chromium } from 'playwright';
import { execSync } from 'child_process';
const run = (c) => { try { return execSync(c + ' 2>&1').toString().trim(); } catch (e) { return 'ERR:' + (e.stdout || '') + (e.stderr || ''); } };
const profile = '/home/silverx/Projects-5/WRAPI/data/profile';
const ctx = await chromium.launchPersistentContext(profile, {
  channel: 'chromium', headless: false, locale: 'en-US',
  args: ['--disable-blink-features=AutomationControlled', '--no-first-run', '--class=wrapi-test'],
  ignoreDefaultArgs: ['--enable-automation'],
});
const page = ctx.pages()[0] || await ctx.newPage();
await page.goto('about:blank');
await page.waitForTimeout(1500);
const addr = JSON.parse(run('hyprctl -j clients')).find(c => c.class === 'wrapi-test')?.address;
const wsOf = () => JSON.parse(run('hyprctl -j clients')).find(c => c.class === 'wrapi-test')?.workspace?.id;
const info = () => { const c = JSON.parse(run('hyprctl -j clients')).find(x => x.class === 'wrapi-test'); return c && { ws: c.workspace?.id, floating: c.floating, at: c.at, size: c.size }; };
console.log('addr:', addr, 'info:', JSON.stringify(info()));

// error-reveal probes for window.move schema
console.log('move({}):', run(`hyprctl eval "hl.dispatch(hl.dsp.window.move({}))"`).slice(0, 200));
console.log('move(addr str):', run(`hyprctl eval "hl.dispatch(hl.dsp.window.move('${addr}'))"`).slice(0, 200));
console.log('move ws number:', run(`hyprctl eval "hl.dispatch(hl.dsp.window.move({address='${addr}', workspace=8, silent=true}))"`).slice(0, 200), JSON.stringify(info()));
console.log('move ws str:', run(`hyprctl eval "hl.dispatch(hl.dsp.window.move({address='${addr}', workspace='8', silent=true}))"`).slice(0, 200), JSON.stringify(info()));
console.log('move rule str:', run(`hyprctl eval "hl.dispatch(hl.dsp.window.move('workspace 8 silent,${addr}'))"`).slice(0, 200), JSON.stringify(info()));
await ctx.close();
