import { chromium } from '@playwright/test';

const rate = Number(process.env.THROTTLE || 20);
const browser = await chromium.launch();
const page = await browser.newPage();
const cdp = await page.context().newCDPSession(page);
await cdp.send('Emulation.setCPUThrottlingRate', { rate });

await page.goto('https://inari.7kgroup.org/', { waitUntil: 'domcontentloaded' });
await page.waitForURL(/auth\.7kgroup\.org/, { timeout: 60000 });
await page.fill('#username', 'dev-admin');
await page.click('#kc-login');
const pw = page.locator('#password');
if (await pw.count()) { await pw.fill('dev-admin'); await page.click('#kc-login'); }
await page.waitForURL(/inari\.7kgroup\.org/, { timeout: 90000 });
await page.waitForTimeout(4000);
await page.getByText('E2E Org', { exact: false }).first().click();
await page.waitForTimeout(2000);
await page.getByRole('link', { name: 'Access', exact: true }).first().click();
await page.waitForTimeout(1500);
await page.getByRole('link', { name: 'Members', exact: true }).first().click();
await page.waitForTimeout(3000);

const name = 'e2e-throttle-' + Date.now().toString(36);
const t0 = Date.now();
await page.fill('#team-name', name);
const respP = page.waitForResponse((r) => r.request().method() === 'POST' && /\/teams$/.test(r.url()), { timeout: 120000 });
await page.getByRole('button', { name: 'Create' }).click();
const resp = await respP;
console.log('POST resolved at', Date.now() - t0, 'ms, status', resp.status());
// poll for the card (attached)
const deadline = Date.now() + 120000;
let found = 0;
while (Date.now() < deadline) {
  const c = await page.getByText(name, { exact: true }).count();
  if (c > 0) { found = Date.now() - t0; break; }
  await page.waitForTimeout(2000);
}
console.log(found ? `card attached after ${found}ms (throttle ${rate}x)` : `NOT FOUND within 120s (throttle ${rate}x)`);
await browser.close();
