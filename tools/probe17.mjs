import { chromium } from 'playwright';
import { execSync } from 'child_process';
const run = (c) => { try { return execSync(c + ' 2>&1').toString().trim(); } catch (e) { return 'ERR:' + (e.stdout||'') + (e.stderr||'') + e.message; } };
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
console.log('window address:', addr);
console.log('active ws before:', JSON.parse(run('hyprctl -j activeworkspace')).id);
const wsOf = () => JSON.parse(run('hyprctl -j clients')).find(c => c.class === 'wrapi-test')?.workspace?.id;
const attempts = [
  [`hl.dispatch(hl.dsp.movetoworkspacesilent('${addr}'))`, 'v1: single arg addr'],
  [`hl.dispatch(hl.dsp.movetoworkspacesilent('8','${addr}'))`, 'v2: two args'],
  [`hl.dispatch('movetoworkspacesilent 8,${addr}')`, 'v3: classic string'],
  [`hl.dispatch(hl.dsp.movetoworkspacesilent('8,${addr}'))`, 'v4: one arg 8+addr'],
];
for (const [lua, label] of attempts) {
  const out = run(`hyprctl eval ${JSON.stringify(lua)}`);
  console.log(label, '->', out.slice(0, 120), '| ws now:', wsOf(), '| active:', JSON.parse(run('hyprctl -j activeworkspace')).id);
  if (wsOf() === 8) { console.log('WORKED with:', label); break; }
}
await ctx.close();
