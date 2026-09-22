// Isolated regressions: no running app or database is required.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const vm = require('node:vm');
const { chromium } = require('playwright');
const { cleanupScenario } = require('./dialog-comparison.cjs');

for (const mode of ['success', 'missing-store', 'open-throw', 'open-error',
  'transaction-throw', 'clear-throw', 'transaction-error', 'transaction-abort',
  'database-close', 'evaluate', 'page-close', 'evaluate-and-close', 'not-queue']) {
  test(`scenario cleanup: ${mode}`, async t => {
    const logged = [];
    t.mock.method(console, 'error', detail => logged.push(detail));
    let databaseCloses = 0, pageCloses = 0, transactions = 0, evaluations = 0;
    const fail = () => new Error(mode);
    const db = {
      objectStoreNames: { contains: name => name === 'queue' && mode !== 'missing-store' },
      transaction(name, access) {
        transactions++;
        assert.equal(name, 'queue');
        assert.equal(access, 'readwrite');
        if (mode === 'transaction-throw') throw fail();
        const tx = {
          objectStore(store) {
            assert.equal(store, 'queue');
            return { clear() {
              if (mode === 'clear-throw') throw fail();
              queueMicrotask(() => {
                if (mode === 'transaction-error') { tx.error = fail(); tx.onerror(); }
                else if (mode === 'transaction-abort') tx.onabort();
                else tx.oncomplete();
              });
            } };
          },
        };
        return tx;
      },
      close() { databaseCloses++; if (mode === 'database-close') throw fail(); },
    };
    const indexedDB = { open(name, version) {
      assert.equal(name, 'treckrr-offline');
      assert.equal(version, 1);
      if (mode === 'open-throw') throw fail();
      const request = { result: db, error: fail() };
      queueMicrotask(() => mode === 'open-error' ? request.onerror() : request.onsuccess());
      return request;
    } };
    const page = {
      async evaluate(callback) {
        evaluations++;
        if (mode.startsWith('evaluate')) throw fail();
        return vm.runInNewContext(`(${callback.toString()})()`, { indexedDB });
      },
      async close() {
        pageCloses++;
        if (['page-close', 'evaluate-and-close'].includes(mode)) throw fail();
      },
    };
    const errors = ['original scenario failure'];
    await cleanupScenario(page, { id: mode === 'not-queue' ? 'drawer' : 'queue-correction' }, errors, 'after/390/dark');
    const failures = ['success', 'missing-store', 'not-queue'].includes(mode) ? 0 : mode === 'evaluate-and-close' ? 2 : 1;
    assert.equal(errors[0], 'original scenario failure');
    assert.equal(errors.length, 1 + failures);
    assert.deepEqual(logged, errors.slice(1));
    for (const detail of logged) {
      assert.match(detail, /^after\/390\/dark\/queue-correction: (queue|page) cleanup failed:/);
      assert.ok(detail.includes(mode === 'transaction-abort' ? 'Queue transaction aborted' : mode));
    }
    assert.equal(pageCloses, 1, 'page closes even when queue cleanup fails');
    assert.equal(evaluations, mode === 'not-queue' ? 0 : 1);
    const opened = !['open-throw', 'open-error', 'evaluate', 'evaluate-and-close', 'not-queue'].includes(mode);
    assert.equal(databaseCloses, opened ? 1 : 0);
    assert.equal(transactions, opened && mode !== 'missing-store' ? 1 : 0);
  });
}

test('gallery uses manifest labels and proportional scrolling in both directions', async () => {
  const template = await fs.readFile(path.join(__dirname, 'dialog-comparison.html'), 'utf8');
  const fixture = {
    before: '<b>revision-before</b>', after: 'revision-after',
    scenarios: [{ id: 'drawer', title: 'Fixture', group: 'Tests', note: 'Unequal image sizes' }],
    shots: ['before', 'after'].map((stage, index) => ({
      id: 'drawer', stage, width: 1280, theme: 'light', url: '/fixture', sha256: stage,
      file: 'data:image/svg+xml,' + encodeURIComponent(`<svg xmlns="http://www.w3.org/2000/svg" width="${1600 + index * 800}" height="${2400 + index * 1600}"/>`),
    })),
  };
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.setContent(template.replace('/*COMPARISON_DATA*/ null', JSON.stringify(fixture)));
    await page.locator('#before-img').evaluate(img => img.decode());
    await page.locator('#after-img').evaluate(img => img.decode());
    assert.equal(await page.locator('#before-label').textContent(), 'Before · ' + fixture.before);
    assert.equal(await page.locator('#after-label').textContent(), 'After · ' + fixture.after);
    assert.equal(await page.locator('#before-label b').count(), 0, 'revision labels are text, not HTML');
    await page.locator('#scale').selectOption('actual');
    const settle = () => page.evaluate(() => new Promise(resolve => {
      requestAnimationFrame(() => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    }));
    const offsets = stage => page.locator(`#${stage}-scroll`).evaluate(node => ({
      top: node.scrollTop, left: node.scrollLeft,
      height: node.scrollHeight - node.clientHeight, width: node.scrollWidth - node.clientWidth,
    }));
    for (const stage of ['before', 'after']) {
      for (const [vertical, horizontal] of [[0.25, 0.75], [1, 1], [0, 0]]) {
        await settle();
        await page.locator(`#${stage}-scroll`).evaluate((node, [y, x]) => {
          node.scrollTop = y * (node.scrollHeight - node.clientHeight);
          node.scrollLeft = x * (node.scrollWidth - node.clientWidth);
        }, [vertical, horizontal]);
        await settle();
        const from = await offsets(stage), to = await offsets(stage === 'before' ? 'after' : 'before');
        assert.ok(from.height > 0 && from.width > 0 && to.height > 0 && to.width > 0);
        assert.notEqual(from.height, to.height);
        assert.notEqual(from.width, to.width);
        assert.ok(Math.abs(to.top - from.top / from.height * to.height) <= 2, 'vertical ratio matches');
        assert.ok(Math.abs(to.left - from.left / from.width * to.width) <= 2, 'horizontal ratio matches');
      }
    }
    await page.locator('#sync').uncheck();
    await page.locator('#after-scroll').evaluate(node => node.scrollTo(80, 100));
    await page.locator('#before-scroll').evaluate(node => node.scrollTo(200, 300));
    await settle();
    assert.equal((await offsets('after')).top, 100);
    assert.equal((await offsets('after')).left, 80);
    await page.locator('#before-img').evaluate(img => { img.style.width = '20px'; img.style.height = '20px'; });
    await settle();
    assert.equal((await offsets('before')).height, 0);
    assert.equal((await offsets('before')).width, 0);
    await page.locator('#sync').check();
    await page.locator('#before-scroll').evaluate(node => node.dispatchEvent(new Event('scroll')));
    await settle();
    assert.equal((await offsets('after')).top, 0, 'zero source height resets destination');
    assert.equal((await offsets('after')).left, 0, 'zero source width resets destination');
    assert.deepEqual(errors, []);
  } finally { await browser.close(); }
});
