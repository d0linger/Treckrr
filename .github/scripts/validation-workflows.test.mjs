import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

const read = (relative) => readFileSync(new URL(relative, import.meta.url), 'utf8').replaceAll('\r\n', '\n');
const baseline = 'f85ca89377f22797dc66f65c37ae37943e18c18d';

function job(source, id) {
  const start = source.indexOf(`\n  ${id}:\n`);
  assert.notEqual(start, -1, `missing job ${id}`);
  const rest = source.slice(start + 1);
  const next = rest.slice(1).search(/^  [\w-]+:\n/m);
  return next < 0 ? rest : rest.slice(0, next + 1);
}

test('parity gate uses complete history and the fixed pre-migration implementation', () => {
  const workflow = read('../workflows/billing-parity.yml');
  assert.match(workflow, /^  workflow_call:$/m);
  assert.match(workflow, /^  workflow_dispatch:$/m);
  assert.match(workflow, /fetch-depth: 0/);
  assert.match(workflow, /persist-credentials: false/);
  assert.ok(workflow.includes(`BILLING_PARITY_BASELINE: ${baseline}`));
  assert.ok(workflow.includes('git cat-file -e "${BILLING_PARITY_BASELINE}^{commit}"'));
  assert.ok(workflow.includes('git fetch --no-tags --depth=1 origin "$BILLING_PARITY_BASELINE"'));
  assert.match(workflow, /Pinned billing baseline is unavailable[\s\S]*exit 1/);
  assert.ok(workflow.includes(`node .github/scripts/billing-parity.mjs ${baseline}`));
  assert.match(workflow, /BILLING_PARITY_RUNTIME_REGRESSIONS: "0"/);
  assert.match(workflow, /node --test .*billing-parity\.test\.mjs .*validation-workflows\.test\.mjs/);
  assert.doesNotMatch(workflow, /secrets:|secrets\.|upload-artifact|contents: write|packages: write/);
});

test('publication needs both financial parity and its own browser validation', () => {
  const workflow = read('../workflows/docker-publish.yml');
  const parity = job(workflow, 'billing-parity');
  assert.match(parity, /uses: \.\/\.github\/workflows\/billing-parity\.yml/);
  assert.doesNotMatch(parity, /\n    if:|continue-on-error:|secrets:/);
  const browser = job(workflow, 'browser-validation');
  assert.match(browser, /if: github.event_name != 'pull_request'/);
  assert.match(browser, /uses: \.\/\.github\/workflows\/e2e\.yml/);
  assert.doesNotMatch(browser, /continue-on-error:|secrets:/);
  const promote = job(workflow, 'promote');
  const needs = promote.match(/needs: \[([^\]]+)\]/)?.[1].split(',').map((name) => name.trim());
  assert.deepEqual(needs, ['build-push', 'scan-candidate', 'billing-parity', 'browser-validation']);
  // No always()/failure() escape: a failed or cancelled required job must block promotion.
  assert.match(promote, /if: github.event_name != 'pull_request'/);
  assert.doesNotMatch(promote, /always\(\)|failure\(\)|cancelled\(\)|continue-on-error:/);
});

test('reusable browser gate preserves existing standalone check triggers', () => {
  const workflow = read('../workflows/e2e.yml');
  assert.match(workflow, /^  workflow_call:$/m);
  assert.match(workflow, /branches: \[main, dev\]/);
  assert.match(workflow, /^  pull_request:$/m);
  assert.match(workflow, /^  workflow_dispatch:$/m);
  assert.match(workflow, /name: Browser smoke \(login → booking → Beleg\)/);
  assert.match(workflow, /run: node e2e\/run\.mjs/);
  assert.doesNotMatch(workflow, /upload-artifact|contents: write|packages: write/);
});

test('local full-suite command cannot silently omit the migration rehearsal', () => {
  const makefile = read('../../Makefile');
  const runner = read('./test-local.sh');
  assert.ok(makefile.includes('-e TEST_INVOICE_MIGRATION_DATABASES="$(TEST_INVOICE_MIGRATION_DATABASES)"'));
  const guard = runner.indexOf('if [ "${TEST_INVOICE_MIGRATION_DATABASES:-}" != "1" ]; then');
  assert.ok(guard >= 0);
  assert.ok(guard < runner.indexOf('apk add'), 'opt-in must fail before setup/database actions');
  assert.match(runner.slice(guard, runner.indexOf('\nfi', guard)), /exit 1/);
  assert.ok(makefile.includes(`node .github/scripts/billing-parity.mjs ${baseline}`));
});

test('scheduled allocation fuzzing is bounded and needs no database or upload', () => {
  const workflow = read('../workflows/billing-fuzz.yml');
  assert.match(workflow, /^  schedule:$/m);
  assert.match(workflow, /^  workflow_dispatch:$/m);
  assert.match(workflow, /-fuzz '\^FuzzPlanInvoiceAllocationConservesMoney\$'/);
  assert.match(workflow, /-fuzztime=45s -parallel=2 -timeout=4m/);
  assert.match(workflow, /TEST_DATABASE_URL: ""/);
  assert.match(workflow, /timeout-minutes: 15/);
  assert.doesNotMatch(workflow, /services:|secrets:|secrets\.|upload-artifact/);
});

test('billing parity runs on the same reviewed image pins as the app', () => {
  // Dependabot only scans the root Dockerfile and Compose files. The parity
  // harness copies their pins, so an update that misses it must fail here
  // instead of leaving the gate on an older image.
  const golang = read('../../Dockerfile').match(/^FROM (golang:\S+@sha256:[0-9a-f]{64})/m)?.[1];
  assert.ok(golang, 'root Dockerfile has no digest-pinned golang image');
  assert.ok(read('./billing-parity.Dockerfile').includes(`FROM ${golang}\n`), 'billing-parity.Dockerfile golang pin drifted');
  const postgres = read('../../docker-compose.yml').match(/image: (postgres:\S+@sha256:[0-9a-f]{64})/)?.[1];
  assert.ok(postgres, 'docker-compose.yml has no digest-pinned postgres image');
  for (const file of ['../../docker-compose.ghcr.yml', '../../compose.e2e.yml', '../../compose.demo.yml']) {
    assert.ok(read(file).includes(`image: ${postgres}\n`), `${file} postgres pin drifted`);
  }
  assert.ok(read('./billing-parity.mjs').includes(`'${postgres}'`), 'billing-parity.mjs postgres pin drifted');
});
