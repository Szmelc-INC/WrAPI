import { chromium } from 'playwright';
import { execSync } from 'child_process';
const run = (c) => { try { return execSync(c + ' 2>&1').toString().trim(); } catch (e) { return 'ERR:' + (e.stdout || '') + (e.stderr || ''); } };
const profile = '/home/silverx/Projects-5/WRAPI/data/profile';
const ctx = await chromium.launchPersistentContext(profile, {
  channel: 'chromium', headless: false, locale: 'en-US',
  args: ['--disable-blink-features=AutomationControlled', '--no-first-run', '--class=wrapi-mini'],
  ignoreDefaultArgs: ['--enable-automation'],
});
const page = ctx.pages()[0] || await ctx.newPage();
await page.goto('about:blank');
await page.waitForTimeout(1500);
const info = () => { const c = JSON.parse(run('hyprctl -j clients')).find(x => x.class === 'wrapi-mini'); return c && { ws: c.workspace?.id, floating: c.floating, at: c.at, size: c.size }; };
const addr = () => JSON.parse(run('hyprctl -j clients')).find(x => x.class === 'wrapi-mini')?.address;
console.log('info:', JSON.stringify(info()));
console.log('resize({}):', run(`hyprctl eval "hl.dispatch(hl.dsp.window.resize({}))"`).slice(0, 160));
console.log('move({}):', run(`hyprctl eval "hl.dispatch(hl.dsp.window.move({}))"`).slice(0, 160));
console.log('float:', run(`hyprctl eval "hl.dispatch(hl.dsp.window.float({address='${addr()}'}))"`).slice(0, 100), JSON.stringify(info()));
const mon = JSON.parse(run('hyprctl -j monitors')).find(m => m.focused) || JSON.parse(run('hyprctl -j monitors'))[0];
console.log('monitor:', JSON.stringify({x: mon.x, y: mon.y, w: mon.width, h: mon.height, id: mon.id}));
const W = 110, H = 110;
const x = mon.x + mon.width - W - 10, y = mon.y + mon.height - H - 10;
console.log('resize exact:', run(`hyprctl eval "hl.dispatch(hl.dsp.window.resize({address='${addr()}', width=${W}, height=${H}, exact=true}))"`).slice(0, 100), JSON.stringify(info()));
console.log('move exact:', run(`hyprctl eval "hl.dispatch(hl.dsp.window.move({address='${addr()}', x=${x}, y=${y}}))"`).slice(0, 100), JSON.stringify(info()));
console.log('active ws:', JSON.parse(run('hyprctl -j activeworkspace')).id);
await ctx.close();
