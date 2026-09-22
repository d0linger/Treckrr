// Validates generated evidence and gallery controls without touching either app.
const { chromium } = require('playwright');
const AxeBuilder = require('@axe-core/playwright').default;
const fs = require('node:fs/promises');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const root = path.resolve(process.argv[2] || 'tmp/dialog-comparison-20260922/view');

async function main() {
  const manifest = JSON.parse(await fs.readFile(path.join(root, 'manifest.json'), 'utf8'));
  assert.deepEqual(manifest.errors, [], 'every capture must succeed');
  assert.equal(manifest.shots.length, manifest.scenarios.length * 8);
  for (const scenario of manifest.scenarios) {
    for (const width of [1280, 390]) for (const theme of ['light', 'dark']) {
      const pair = manifest.shots.filter(s => s.id === scenario.id && s.width === width && s.theme === theme);
      assert.deepEqual(pair.map(s => s.stage).sort(), ['after', 'before']);
      for (const shot of pair) {
        const png = await fs.readFile(path.join(root, shot.file));
        assert.equal(png.subarray(1, 4).toString(), 'PNG');
        assert.ok(png.readUInt32BE(16) > 0 && png.readUInt32BE(20) > 0);
        assert.equal(crypto.createHash('sha256').update(png).digest('hex'), shot.sha256);
      }
    }
  }
  const browser = await chromium.launch({ headless: true });
  const errors = [];
  try {
    const context = await browser.newContext({ viewport: { width: 1600, height: 1050 } });
    const page = await context.newPage();
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(pathToFileURL(path.join(root, 'index.html')).href);
    await page.locator('#theme').selectOption('dark');
    await page.locator('#width').selectOption('390');
    await page.locator('#after-img').evaluate(img => img.decode());
    assert.match(await page.locator('#after-img').getAttribute('src'), /drawer-390-dark-after/);
    await page.screenshot({ path: path.join(root, 'gallery-desktop.png') });
    await page.locator('#search').fill('invoice');
    assert.ok(await page.locator('#surfaces button').count() >= 3);
    await page.locator('#surfaces button').filter({ hasText: 'Invoice correction destination' }).click();
    assert.match(await page.locator('#after-img').getAttribute('src'), /invoice-repair/);
    await page.locator('#scale').selectOption('actual');
    assert.equal(await page.locator('#panes').getAttribute('class'), 'panes actual');
    await page.locator('#scale').selectOption('fit');
    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator('#mobile-surface').selectOption('booking-equipment-in');
    await page.locator('#before-img').evaluate(img => img.decode());
    assert.equal(await page.locator('#surface-title').textContent(), 'equipment · Nachbar verrechnet');
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1));
    await page.screenshot({ path: path.join(root, 'gallery-mobile.png'), fullPage: true });
    const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21aa']).analyze();
    assert.deepEqual(axe.violations.filter(v => ['serious', 'critical'].includes(v.impact)), []);
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ scenarios: manifest.scenarios.length, screenshots: manifest.shots.length, galleryChecks: 'passed', browserErrors: errors }, null, 2));
  } finally { await browser.close(); }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
