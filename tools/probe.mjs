import { chromium } from 'playwright';
import fs from 'fs';

const profile = '/home/silverx/Projects-5/WRAPI/data/profile';
const outDir = '/home/silverx/Projects-5/WRAPI/data/probe';
fs.mkdirSync(outDir, { recursive: true });

const ctx = await chromium.launchPersistentContext(profile, {
  args: ['--disable-blink-features=AutomationControlled', '--no-first-run', '--no-default-browser-check'],
  ignoreDefaultArgs: ['--enable-automation'],
  channel: 'chromium',
  headless: process.env.PROBE_HEADLESS !== '0',
  locale: 'en-US',
  viewport: { width: 1440, height: 900 },
});
const page = ctx.pages()[0] || await ctx.newPage();
try {
  await page.goto('https://www.google.com/search?q=What+is+the+capital+of+Poland%3F&udm=50&csuir=1', { waitUntil: 'domcontentloaded', timeout: 45000 });
  await page.waitForTimeout(9000);
  console.log('URL:', page.url());
  console.log('TITLE:', await page.title());
  const html = await page.content();
  fs.writeFileSync(outDir + '/page.html', html);
  console.log('HTML bytes:', html.length);
} catch (e) {
  console.error('ERR:', e.message);
  fs.writeFileSync(outDir + '/page.html', await page.content().catch(() => ''));
}
await ctx.close();
