import { chromium } from 'playwright';
import { execSync } from 'child_process';
const run = (c) => { try { return execSync(c + ' 2>&1').toString().trim(); } catch { return 'ERR'; } };
const profile = '/home/silverx/Projects-5/WRAPI/data/profile';
const clients = () => run(`hyprctl -j clients`).toString();

// launch with candidate unique-id flags under default (wayland) ozone
const ctx = await chromium.launchPersistentContext(profile, {
  channel: 'chromium', headless: false, locale: 'en-US',
  args: ['--disable-blink-features=AutomationControlled', '--no-first-run',
         '--class=wrapi-test', '--wayland-app-id=wrapi-test'],
  ignoreDefaultArgs: ['--enable-automation'],
});
const page = ctx.pages()[0] || await ctx.newPage();
await page.goto('about:blank');
await page.waitForTimeout(2000);
const json = clients();
try {
  const arr = JSON.parse(json);
  for (const c of arr) {
    if ((c.class + '').includes('wrapi') || (c.initialClass + '').includes('wrapi') || (c.class + '').toLowerCase().includes('chrom')) {
      console.log('MATCH:', JSON.stringify({class: c.class, initialClass: c.initialClass, appid: c.appId ?? c.appid, initialTitle: c.initialTitle, title: c.title, workspace: c.workspace?.id, pid: c.pid, address: c.address, floating: c.floating}));
    }
  }
  console.log('total clients:', arr.length);
} catch (e) { console.log('clients parse fail:', json.slice(0, 500)); }
await ctx.close();
