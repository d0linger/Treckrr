// Captures two already-running, disposable revisions. Never point this at user data.
// Usage: node e2e/review/dialog-comparison.cjs [output-directory]
const { chromium } = require('playwright');
const fs = require('node:fs/promises');
const path = require('node:path');
const crypto = require('node:crypto');
const output = path.resolve(process.argv[2] || 'tmp/dialog-comparison-20260922/view');
const revisions = [
  { stage: 'before', sha: 'a2eebac', origin: 'http://localhost:18080' },
  { stage: 'after', sha: 'be401d0', origin: 'http://localhost:18081' },
];
const unchanged = 'No direct visual change in this polish. Shared navigation and form-error improvements still apply.';
const scenarios = [];
function add(id, title, route, group, note = unchanged, setup, options = {}) {
  scenarios.push({ id, title, route, group, note, setup, ...options });
}
const routes = [
  ['dashboard', 'Dashboard', '/?year=1'],
  ['neighbor', 'Neighbour / booking overview', '/neighbors/1?year=1'],
  ['beleg', 'Beleg / settlement', '/neighbors/1/beleg?year=1', 'Print/save and sharing/delivery actions are now separate groups.'],
  ['history', 'Neighbour history', '/neighbors/1/overview'],
  ['entries', 'Booking list / filters', '/buchungen?year=1'],
  ['stats', 'Statistics', '/stats?year=1'],
  ['stats-all', 'Cross-year statistics', '/stats/all'],
  ['neighbors', 'Neighbour management', '/neighbors'],
  ['persons', 'Person master data', '/personen'],
  ['dunning', 'Dunning overview', '/mahnwesen?year=1'],
  ['journal', 'Invoice journal', '/rechnungsjournal?year=1'],
  ['years', 'Billing years', '/years'],
  ['closing', 'Year-closing checklist', '/years/1/abschluss', 'Complete closing explanations wrap instead of being truncated, especially on mobile.'],
  ['batch', 'Batch invoicing', '/years/1/issue-all'],
  ['recalc-year', 'Year recalculation', '/years/1/recalc'],
  ['prices', 'Price master data', '/prices?base=1'],
  ['compare', 'Price comparison', '/prices/compare?base=1'],
  ['rigs', 'Fixed rigs', '/gespanne?base=1'],
  ['bases', 'Assessment bases', '/bases'],
  ['recurring', 'Recurring bookings', '/recurring'],
  ['booking-edit', 'Booking edit / attachments', '/entries/1/edit'],
  ['booking-copy', 'Booking copy', '/entries/1/copy'],
  ['payment-edit', 'Payment edit', '/payments/1/edit'],
  ['payment-copy', 'Payment copy', '/payments/1/copy'],
  ['ledger-edit', 'Legacy ledger edit', '/ledger/1/edit', 'The direction radio now has a visible keyboard-focus ring; see the focused state.'],
  ['ledger-copy', 'Legacy ledger copy', '/ledger/1/copy'],
  ['recalc-neighbor', 'Neighbour recalculation', '/neighbors/1/recalc?year=1'],
  ['invoice-missing', 'Invoice / missing details', '/neighbors/1/invoice/confirm?year=1', 'The correction link now says “Nachbardaten ergänzen” and opens the matching editor.'],
  ['invoice-ready', 'Invoice / ready to issue', '/neighbors/2/invoice/confirm?year=1', '“Snapshot-Vorschau” becomes “Rechnungsvorschau”; immutable invoice guidance is plain German.'],
  ['reminder', 'Payment reminder letter', '/neighbors/3/mahnung?year=1'],
  ['csv-import', 'CSV booking import', '/entries/import?year=1'],
  ['bank-import', 'Bank payment import', '/payments/import'],
  ['users', 'User administration', '/admin/users'],
  ['backup', 'Backup / restore', '/admin/backup'],
  ['company', 'Company details', '/admin/company'],
  ['audit', 'Audit log', '/admin/audit'],
  ['profile', 'Profile / security / appearance', '/profile'],
  ['password', 'Password change', '/account/password'],
  ['two-factor', 'Two-factor setup', '/account/2fa', 'No direct visual change. Random setup QR codes are masked in both captures.'],
];
for (const [id, title, route, note] of routes) add(id, title, route, 'Pages & forms', note);
add('login', 'Login', '/login', 'Authentication', unchanged, null, { public: true });
add('login-2fa', 'Login / authenticator code', '/login', 'Authentication', unchanged, null, { public: true, pending: true });
add('login-recovery', 'Login / backup code', '/login', 'Authentication', unchanged,
  async p => p.locator('[data-2fa-toggle]').click(), { public: true, pending: true });
add('offline', 'Offline page', '/offline', 'System states', unchanged, null, { public: true });
add('not-found', 'Not-found page', '/missing-comparison-page', 'System states', unchanged, null, { status: 404 });
add('drawer', 'Navigation drawer', '/?year=1', 'Dialogs', 'Destinations are grouped by task with quiet headings.',
  async p => { await p.locator('[data-drawer-open]').click(); await p.locator('#drawer[aria-hidden="false"]').waitFor(); }, { viewport: true });
add('search', 'Search palette', '/?year=1', 'Dialogs', unchanged,
  async p => { await p.keyboard.press('Control+k'); await p.locator('.cmdk input').fill('Demo'); await p.locator('.cmdk__item').first().waitFor(); }, { viewport: true });
add('shortcuts', 'Keyboard shortcut help', '/?year=1', 'Dialogs', unchanged,
  async p => { await p.keyboard.press('?'); await p.locator('#kbd-help').waitFor(); }, { viewport: true });
add('confirm', 'Standard confirmation', '/neighbors/2/invoice/confirm?year=1', 'Dialogs', 'Confirmation layout is unchanged; nothing is confirmed or saved.',
  async p => { await p.locator('form[data-confirm] button[type="submit"]').click(); await p.locator('#confirmModal[open]').waitFor(); }, { viewport: true });
add('confirm-reason', 'Confirmation / reason', '/neighbors/1?year=1', 'Dialogs', 'Same confirmation component with an optional reason; cancellation only.',
  async p => { await p.locator('form[action="/ledger/1/void"] button').click(); await p.locator('#confirmModal[open]').waitFor(); }, { viewport: true });
add('confirm-linked', 'Confirmation / linked person', '/neighbors/1?year=1', 'Dialogs', 'Same confirmation component with reason and linked-booking checkbox; cancellation only.',
  async p => { await p.locator('form[action="/entries/1/void"] button').click(); await p.locator('#confirmModal[open]').waitFor(); }, { viewport: true });
add('quick', 'Expanded quick entry', '/neighbors/1?year=1', 'Expanded forms', 'Wider fields, mobile scroll guidance, and row-specific accessible names (also on cloned rows).',
  async p => { await p.getByText('Schnellerfassung (mehrere Zeilen)', { exact: true }).click(); }, { target: '[data-quick-form]' });
add('invoice-repair', 'Invoice correction destination', '/neighbors/1/invoice/confirm?year=1', 'Expanded forms', 'Before: booking page. After: correct neighbour editor with contact details already open.',
  async p => { await p.getByRole('link', { name: /Nachbardaten/ }).click(); }, { target: 'main' });
add('ledger-focus', 'Legacy direction / keyboard focus', '/ledger/1/edit', 'Expanded forms', 'The selected direction now gets a visible blue keyboard-focus ring.',
  async p => { await p.keyboard.press('Tab'); await p.locator('input[name="direction"]').first().focus(); }, { target: 'main' });
for (const direction of ['out', 'in']) {
  for (const kind of ['equipment', 'labor', 'quantity', 'fixed']) {
    add(`booking-${kind}-${direction}`, `${kind} · ${direction === 'out' ? 'Ich verrechne' : 'Nachbar verrechnet'}`,
      '/neighbors/1?year=1', 'Booking variants', 'No new booking-form redesign in this polish. Both revisions already share the machine/person rates and itemized costs.', async p => {
        const form = p.locator('[data-unified-booking]');
        await form.locator(`[name="booking_direction"][value="${direction}"]`).check();
        await form.locator('[name="booking_kind"]').selectOption(kind);
        await form.locator('[name="entry_date"]').fill('2025-09-21');
        if (kind === 'equipment') await form.locator('[name="gespann_id"]').selectOption('1');
        if (['equipment', 'labor'].includes(kind)) {
          await form.locator('[name="hours"]').fill('4');
          if (kind === 'equipment') {
            await form.locator('[data-person-details] summary').click();
            if (!await form.locator('[data-person-row]:visible').count()) await form.locator('[data-person-add]').click();
          }
          await form.locator('[data-person-row]:visible [name="person_id"]').first().selectOption('1');
        } else if (kind === 'quantity') {
          await form.locator('[name="unit"]').selectOption('Ballen');
          await form.locator('[name="quantity"]').fill('10');
          await form.locator('[name="unit_price"]').fill('5');
          await form.locator('[name="task_label"]').fill('Ballen pressen (Demo)');
        } else {
          await form.locator('[name="amount"]').fill('30');
          await form.locator('[name="task_label"]').fill('Materialkosten (Demo)');
        }
        await p.waitForTimeout(250);
      }, { target: '[data-unified-booking]' });
  }
}
for (const state of ['queue', 'queue-correction']) {
  add(state, state === 'queue' ? 'Offline queue / rejected booking' : 'Offline queue / correction', '/?year=1', 'Dialogs',
    'Real queue UI with a synthetic rejected item. No replay is sent. Dark offline-counter text has improved contrast.', async p => {
      await p.evaluate(async () => {
        const request = indexedDB.open('treckrr-offline', 1);
        const db = await new Promise((resolve, reject) => { request.onsuccess = () => resolve(request.result); request.onerror = () => reject(request.error); });
        const tx = db.transaction('queue', 'readwrite');
        tx.objectStore('queue').put({ id: 'comparison-only', user: document.querySelector('meta[name="user-id"]').content,
          path: '/entries', at: 1758412800000, rejection: { status: 422, message: 'Bitte die Personenstunden prüfen.' },
          data: { entry_date: '2025-09-21', booking_kind: 'equipment', booking_direction: 'in', task_label: 'Betonmischen (Demo)', hours: '4', year_id: '1', neighbor_id: '1', gespann_id: '1' } });
        await new Promise((resolve, reject) => { tx.oncomplete = resolve; tx.onerror = reject; }); db.close();
        const badge = document.querySelector('[data-offline-badge]'); badge.hidden = false; badge.textContent = '1'; badge.click();
      });
      await p.locator('[data-offline-panel]:visible .offlineq__row').waitFor();
      if (state === 'queue-correction') await p.getByText('Buchungsdaten korrigieren', { exact: true }).click();
    }, { viewport: true });
}
const probeMarkup = `<section id="comparison-probe" class="card stack">
  <h2 class="section-title">Formular-Rückmeldung (Demo)</h2>
  <label class="field"><span>Stunden</span><input id="comparison-number" class="input" type="number" min="1" max="10" step="0.5" value="0"><span class="muted small">Mindestens 1 Stunde.</span></label>
  <label class="field"><span>E-Mail</span><input id="comparison-email" class="input" type="email" value="keine-adresse"></label>
  <div class="step step--done"><span class="step__n">1</span><span class="step__t">Stammdaten vollständig</span></div>
</section><div class="toast" role="status"><span>Demo-Buchung gespeichert.</span><form class="toast__undo"><button class="btn btn--ghost btn--sm" type="button">Rückgängig</button></form><button type="button" data-toast-dismiss aria-label="Meldung schließen">×</button></div>`;
add('errors', 'Inline validation messages', '/profile', 'Feedback states', 'Generic errors become specific numeric and email guidance. Demonstration fields use the real shared CSS/JS.',
  async p => { await p.locator('#comparison-number').evaluate(n => n.reportValidity()); await p.locator('#comparison-email').evaluate(n => n.reportValidity()); }, { probe: true, target: '#comparison-probe' });
add('undo', 'Undo after six seconds', '/profile', 'Feedback states', 'Before: actionable toast disappears. After: Undo stays available until dismissal. Identical synthetic toast, real scripts.',
  async p => { await p.waitForTimeout(6200); }, { probe: true, viewport: true });
add('skip', 'Focused skip link', '/?year=1', 'Feedback states', 'Dark theme: the skip link now uses a contrast-safe foreground.',
  async p => { await p.locator('.skip').focus(); }, { viewport: true });

async function cleanupScenario(page, scenario, errors, prefix) {
  const recordError = (step, error) => {
    const detail = `${prefix}/${scenario.id}: ${step} cleanup failed: ${error.message}`;
    errors.push(detail);
    console.error(detail);
  };
  try {
    if (scenario.id.startsWith('queue')) await page.evaluate(async () => {
      let db;
      try {
        const request = indexedDB.open('treckrr-offline', 1);
        db = await new Promise((resolve, reject) => {
          request.onsuccess = () => resolve(request.result);
          request.onerror = () => reject(request.error);
        });
        if (!db.objectStoreNames.contains('queue')) return;
        const tx = db.transaction('queue', 'readwrite');
        await new Promise((resolve, reject) => {
          tx.oncomplete = resolve;
          tx.onerror = () => reject(tx.error || new Error('Queue transaction failed'));
          tx.onabort = () => reject(tx.error || new Error('Queue transaction aborted'));
          tx.objectStore('queue').clear();
        });
      } finally {
        if (db) db.close();
      }
    });
  } catch (error) {
    recordError('queue', error);
  } finally {
    try { await page.close(); }
    catch (error) { recordError('page', error); }
  }
}

async function captureRevision(browser, revision) {
  const loginContext = await browser.newContext({ baseURL: revision.origin, serviceWorkers: 'block' });
  const login = await loginContext.newPage();
  await login.goto('/login');
  await login.locator('[name="username"]').fill('admin');
  await login.locator('[name="password"]').fill('e2e-admin-password-123');
  await login.getByRole('button', { name: 'Anmelden', exact: true }).click();
  await login.locator('.appbar').waitFor();
  const storageState = await loginContext.storageState();
  await loginContext.close();
  const batches = [];
  for (const theme of ['light', 'dark']) batches.push(...await Promise.all([1280, 390].map(async width => {
    const context = await browser.newContext({ baseURL: revision.origin, storageState, serviceWorkers: 'block',
      viewport: { width, height: 900 }, colorScheme: theme, reducedMotion: 'reduce', locale: 'de-AT', timezoneId: 'Europe/Vienna' });
    await context.addInitScript(({ theme }) => {
      localStorage.setItem('treckrr-theme', theme);
      localStorage.setItem('treckrr-appbg', 'werkraster');
      sessionStorage.setItem('treckrr-appbg-s', '1');
      Date.now = () => 1790035200000;
      // Seed only ordinary Math.random (decorative canvases), not cryptographic randomness.
      let seed = 7391;
      Math.random = () => ((seed = (seed * 16807) % 2147483647) - 1) / 2147483646;
    }, { theme });
    const errors = [];
    await context.route('**/*', route => {
      if (!['GET', 'HEAD'].includes(route.request().method())) {
        errors.push(`Blocked unexpected write: ${route.request().method()} ${route.request().url()}`);
        return route.abort();
      }
      return route.continue();
    });
    const rows = [];
    const selectedScenarios = process.env.COMPARISON_COMPONENTS ? scenarios.filter(s => s.target) : scenarios;
    for (const scenario of selectedScenarios) {
      const page = await context.newPage();
      page.setDefaultTimeout(10000);
      try {
        if (scenario.public) await context.clearCookies();
        else await context.addCookies(storageState.cookies);
        if (scenario.pending) {
          const payload = `2fa:1|${Math.floor(Date.now() / 1000) + 300}`;
          const value = Buffer.from(payload).toString('base64url') + '.' + crypto.createHmac('sha256', 'comparison-local-secret-at-least-32-bytes').update(payload).digest('hex');
          await context.addCookies([{ name: 'treckrr_2fa', value, url: revision.origin }]);
        }
        if (scenario.probe) await page.route('**/profile', async route => {
          const response = await route.fetch();
          await route.fulfill({ response, body: (await response.text()).replace('</main>', probeMarkup + '</main>') });
        });
        const response = await page.goto(scenario.route);
        if (response.status() !== (scenario.status || 200)) throw new Error(`HTTP ${response.status()}`);
        await page.evaluate(() => document.fonts.ready);
        if (scenario.id.startsWith('login')) {
          const pause = page.locator('#login-bg-toggle');
          if (await pause.count()) await pause.click();
        }
        if (scenario.setup) await scenario.setup(page);
        await page.evaluate(() => document.fonts.ready);
        const file = `${scenario.id}-${width}-${theme}-${revision.stage}.png`;
        const target = scenario.target ? page.locator(scenario.target) : page;
        // Isolate the component without injecting a stylesheet (the app's CSP
        // correctly rejects screenshot style injection). The page is disposable.
        if (scenario.target) await page.evaluate(() => {
          document.querySelectorAll('.topstack,.tabbar,.fab').forEach(node => node.remove());
        });
        const buffer = await target.screenshot({ path: path.join(output, 'images', file), animations: 'disabled',
          ...(scenario.target ? {} : { fullPage: !scenario.viewport }), mask: [page.locator('.qrwrap')], maskColor: '#737b75' });
        rows.push({ id: scenario.id, width, theme, stage: revision.stage, file: `images/${file}`,
          sha256: crypto.createHash('sha256').update(buffer).digest('hex'), url: new URL(page.url()).pathname + new URL(page.url()).search });
      } catch (error) {
        const detail = `${revision.stage}/${width}/${theme}/${scenario.id}: ${error.message}`;
        errors.push(detail);
        console.error(detail);
      } finally {
        await cleanupScenario(page, scenario, errors, `${revision.stage}/${width}/${theme}`);
      }
    }
    await context.close();
    console.log(`${revision.stage} ${width} ${theme}: ${rows.length}/${selectedScenarios.length} captured`);
    return { rows, errors };
  })));
  return batches;
}

async function main() {
  await fs.mkdir(path.join(output, 'images'), { recursive: true });
  if (process.env.COMPARISON_GALLERY_ONLY) {
    const manifest = JSON.parse(await fs.readFile(path.join(output, 'manifest.json'), 'utf8'));
    const template = await fs.readFile(path.join(__dirname, 'dialog-comparison.html'), 'utf8');
    await fs.writeFile(path.join(output, 'index.html'), template.replace('/*COMPARISON_DATA*/ null', JSON.stringify(manifest).replace(/</g, '\\u003c')));
    return;
  }
  const browser = await chromium.launch({ headless: true });
  let results;
  try {
    results = [];
    for (const revision of revisions) results.push(...await captureRevision(browser, revision));
  }
  finally { await browser.close(); }
  const errors = results.flatMap(r => r.errors);
  let shots = results.flatMap(r => r.rows);
  if (process.env.COMPARISON_COMPONENTS) {
    const previous = JSON.parse(await fs.readFile(path.join(output, 'manifest.json'), 'utf8'));
    const replaced = new Set(scenarios.filter(s => s.target).map(s => s.id));
    shots = previous.shots.filter(s => !replaced.has(s.id)).concat(shots);
    // The earlier crop attempt's injected stylesheet was blocked by CSP. All
    // those component captures are now replaced without style injection; its
    // raw failure manifest is retained separately, not counted as a new failure.
    errors.push(...previous.errors.filter(e => !e.endsWith('/csp-report') && ![...replaced].some(id => e.includes('/' + id + ':'))));
  }
  const manifest = { before: revisions[0].sha, after: revisions[1].sha, generated: new Date().toISOString(),
    scenarios: scenarios.map(({ setup, ...s }) => s), shots, errors };
  await fs.writeFile(path.join(output, 'manifest.json'), JSON.stringify(manifest, null, 2));
  const template = await fs.readFile(path.join(__dirname, 'dialog-comparison.html'), 'utf8');
  await fs.writeFile(path.join(output, 'index.html'), template.replace('/*COMPARISON_DATA*/ null', JSON.stringify(manifest).replace(/</g, '\\u003c')));
  console.log(JSON.stringify({ scenarios: scenarios.length, screenshots: shots.length, errors, output }, null, 2));
  if (errors.length) process.exitCode = 1;
}
if (require.main === module) main().catch(error => { console.error(error); process.exitCode = 1; });
module.exports = { cleanupScenario };
