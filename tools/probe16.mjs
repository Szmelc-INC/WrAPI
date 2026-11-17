import { chromium } from 'playwright';
import { execSync } from 'child_process';
const run = (c) => { try { return execSync(c + ' 2>&1').toString().trim(); } catch { return 'ERR:' + c; } };
const profile = '/home/silverx/Projects-5/WRAPI/data/profile';
const activeWs = () => JSON.parse(run('hyprctl -j activeworkspace')).id;

// rules for two test classes
console.log('rule ws:', run(`hyprctl keyword windowrulev2 "workspace 8 silent,class:^wrapi-ws$"`).split('\n')[0]);
console.log('rule float:', run(`hyprctl keyword windowrulev2 "float,class:^wrapi-mini$"`).split('\n')[0]);
console.log('rule size:', run(`hyprctl keyword windowrulev2 "size 110 110,class:^wrapi-mini$"`).split('\n')[0]);
console.log('rule move:', run(`hyprctl keyword windowrulev2 "move 1770 930,class:^wrapi-mini$"`).split('\n')[0]);

const before = activeWs();
const launch = async (cls) => {
  const ctx = await chromium.launchPersistentContext(profile, {
    channel: 'chromium', headless: false, locale: 'en-US',
    args: ['--disable-blink-features=AutomationControlled', '--no-first-run', `--class=${cls}`, '--window-position=100,100'],
    ignoreDefaultArgs: ['--enable-automation'],
  });
  const page = ctx.pages()[0] || await ctx.newPage();
  await page.goto('about:blank');
  await page.waitForTimeout(1500);
  return ctx;
};

const wsCtx = await launch('wrapi-ws');
console.log('active ws before/after ws-launch:', before, activeWs());
const clients = JSON.parse(run('hyprctl -j clients'));
const wsw = clients.find(c => c.class === 'wrapi-ws');
console.log('ws window:', wsw && { ws: wsw.workspace?.id, hidden: wsw.hidden, at: wsw.at, size: wsw.size });

const miniCtx = await launch('wrapi-mini');
console.log('active ws after mini-launch:', activeWs());
const clients2 = JSON.parse(run('hyprctl -j clients'));
const mw = clients2.find(c => c.class === 'wrapi-mini');
console.log('mini window:', mw && { ws: mw.workspace?.id, floating: mw.floating, at: mw.at, size: mw.size });

await wsCtx.close();
await miniCtx.close();
