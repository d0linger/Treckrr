// Run real old/new store implementations, then migrate an old-created database.
// All database writes are confined to a private network-less Docker container.
// Usage: node .github/scripts/billing-parity.mjs [baseline-commit]
import { spawn, spawnSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { compareBillingParity } from './billing-parity-compare.mjs';
import { sameDaySnapshots } from './billing-parity-clock.mjs';

const runtimeRegressions = process.env.BILLING_PARITY_RUNTIME_REGRESSIONS ?? '1';
if (!['0', '1'].includes(runtimeRegressions)) {
  throw new Error('BILLING_PARITY_RUNTIME_REGRESSIONS must be 0 or 1');
}

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const git = (...args) => {
  const result = spawnSync('git', args, { cwd: root, encoding: 'utf8', shell: false });
  if (result.status !== 0) throw new Error(result.stderr || 'git failed');
  return result.stdout.trim();
};
const baseline = git('rev-parse', '--verify', `${process.argv[2] || 'f85ca89'}^{commit}`);
const current = git('rev-parse', 'HEAD');
const id = `treckrr-parity-${randomBytes(6).toString('hex')}`;
const reportDir = path.join(root, 'tmp', 'billing-parity', id);
fs.mkdirSync(reportDir, { recursive: true });
const legacyDir = path.join(reportDir, 'before');
fs.mkdirSync(legacyDir);
const dbName = `${id}-db`;
const cacheName = `${id}-cache`;
const images = [];
const activeTests = new Set();
let interrupted = false;
let activeChild;
const interrupt = () => {
  interrupted = true;
  activeChild?.kill('SIGTERM');
};
process.on('SIGINT', interrupt);
process.on('SIGTERM', interrupt);
let databaseCreated = false;
let cacheCreated = false;
const dockerfile = path.join(root, '.github/scripts/billing-parity.Dockerfile');
const testFiles = [
  'internal/store/billing_compatibility_integration_test.go',
  'internal/store/billing_upgrade_parity_integration_test.go',
];
function sourceManifest(directory, relative = '') {
  return Object.fromEntries(fs.readdirSync(path.join(directory, relative), { withFileTypes: true })
    .sort((left, right) => left.name.localeCompare(right.name)).flatMap(entry => {
      const file = path.posix.join(relative, entry.name);
      if (entry.isSymbolicLink()) throw new Error(`Source symlink is not supported: ${file}`);
      if (entry.isDirectory()) return Object.entries(sourceManifest(directory, file));
      return [[file, createHash('sha256').update(fs.readFileSync(path.join(directory, file))).digest('hex')]];
    }));
}
const metadata = {
  baseline, current, currentWorkingDiffSHA256: createHash('sha256')
    .update(git('diff', '--no-ext-diff', 'HEAD', '--', 'cmd', 'internal')).digest('hex'),
  testSHA256: Object.fromEntries(testFiles.map(file => [file, createHash('sha256')
    .update(fs.readFileSync(path.join(root, file))).digest('hex')])),
  runnerSHA256: Object.fromEntries(['billing-parity.mjs', 'billing-parity-compare.mjs', 'billing-parity-clock.mjs'].map(file =>
    [file, createHash('sha256').update(fs.readFileSync(path.join(root, '.github/scripts', file))).digest('hex')])),
  currentSourceSHA256: { ...sourceManifest(root, 'cmd'), ...sourceManifest(root, 'internal'),
    ...Object.fromEntries(['go.mod', 'go.sum', '.dockerignore', '.github/scripts/billing-parity.Dockerfile'].map(file =>
      [file, createHash('sha256').update(fs.readFileSync(path.join(root, file))).digest('hex')])) },
};
fs.writeFileSync(path.join(reportDir, 'metadata.json'), JSON.stringify(metadata, null, 2));

async function run(command, args, logName, { quiet = false, cleanup = false } = {}) {
  if (interrupted && !cleanup) throw new Error('Comparison interrupted; cleaning up isolated resources');
  const chunks = [];
  const log = fs.createWriteStream(path.join(reportDir, `${logName}.log`));
  try {
    const code = await new Promise((resolve, reject) => {
      const child = spawn(command, args, { cwd: root, shell: false, windowsHide: true });
      if (!cleanup) activeChild = child;
      const receive = chunk => { chunks.push(chunk); log.write(chunk); };
      child.stdout.on('data', receive);
      child.stderr.on('data', receive);
      child.on('error', reject);
      child.on('close', resolve);
    });
    const output = Buffer.concat(chunks).toString('utf8');
    if (code !== 0 || (interrupted && !cleanup)) throw new Error(`${logName} failed (${code}):\n${output.slice(-5000)}`);
    if (!quiet) console.log(`${logName}: passed`);
    return output;
  } finally {
    if (!cleanup) activeChild = undefined;
    await new Promise(resolve => log.end(resolve));
  }
}

const records = (output, marker) => output.split(/\r?\n/)
  .filter(line => line.includes(marker)).map(line => JSON.parse(line.slice(line.indexOf(marker) + marker.length)));

async function test(image, label, args, phase = '') {
  const name = `${id}-${label}`;
  activeTests.add(name);
  const database = phase ? 'treckrr_parity_upgrade' : 'treckrr_parity_admin';
  const env = [
    '-e', 'TZ=UTC',
    '-e', `TEST_DATABASE_URL=postgres://treckrr_test:synthetic-test-only@127.0.0.1:5432/${database}?sslmode=disable`,
    '-e', 'TEST_INVOICE_MIGRATION_DATABASES=1',
  ];
  if (phase) env.push('-e', `TEST_BILLING_UPGRADE_PHASE=${phase}`, '-e', 'TEST_BILLING_UPGRADE_ALLOWED=1');
  try {
    return await run('docker', ['run', '--rm', '--name', name, '--label', `treckrr.validation=${id}`,
      '--network', `container:${dbName}`, '--mount', `type=volume,source=${cacheName},target=/root/.cache/go-build`,
      ...env, image, 'go', 'test', '-race', '-tags=integration', '-p', '1', '-count=1', '-timeout=15m', '-v', ...args], label);
  } finally {
    // Keep the name until final cleanup if the process failed or was interrupted.
    const state = spawnSync('docker', ['ps', '-aq', '--filter', `name=^/${name}$`], { encoding: 'utf8', windowsHide: true });
    if (state.status === 0 && !state.stdout.trim()) activeTests.delete(name);
  }
}

try {
  console.log(`Billing comparison: ${baseline.slice(0, 7)} -> ${current.slice(0, 7)} + current source\nReports: ${reportDir}`);
  const archive = path.join(reportDir, 'before.tar');
  await run('git', ['archive', '--format=tar', `--output=${archive}`, baseline, 'go.mod', 'go.sum', 'cmd', 'internal'], 'archive');
  await run('tar', ['-xf', archive, '-C', legacyDir], 'extract');
  for (const file of testFiles) fs.copyFileSync(path.join(root, file), path.join(legacyDir, file));
  for (const [label, context] of [['before', legacyDir], ['after', root]]) {
    const image = `${id}-${label}:test`;
    images.push(image);
    await run('docker', ['build', '-f', dockerfile, '-t', image, context], `build-${label}`);
  }
  await run('docker', ['run', '-d', '--rm', '--name', dbName, '--label', `treckrr.validation=${id}`,
    '--network', 'none', '--tmpfs', '/var/lib/postgresql/data',
    '-e', 'POSTGRES_USER=treckrr_test', '-e', 'POSTGRES_PASSWORD=synthetic-test-only', '-e', 'POSTGRES_DB=treckrr_parity_admin',
    'postgres:16-alpine@sha256:f4610795587492cd4fb887c2dc3de301b813278bb6f616992061141b5f15ac36'], 'database');
  databaseCreated = true;
  let ready = false;
  for (let attempt = 0; attempt < 60; attempt++) {
    const probe = spawnSync('docker', ['exec', dbName, 'pg_isready', '-U', 'treckrr_test', '-d', 'treckrr_parity_admin'], { windowsHide: true });
    if (probe.status === 0) { ready = true; break; }
    await new Promise(resolve => setTimeout(resolve, 1000));
  }
  if (!ready) throw new Error('Disposable PostgreSQL did not become ready');
  await run('docker', ['volume', 'create', '--label', `treckrr.validation=${id}`, cacheName], 'cache');
  cacheCreated = true;
  const { before, after, matrixUTCDate, matrixAttempts } = await sameDaySnapshots(async attempt => {
    const suffix = attempt ? '-after-midnight' : '';
    if (attempt) console.warn('UTC date changed during comparison; repeating both complete matrices in fresh scratch databases.');
    return {
      before: records(await test(images[0], `matrix-before${suffix}`, ['-run', '^TestBillingCompatibility', './internal/store']), 'BILLING_PARITY '),
      after: records(await test(images[1], `matrix-after${suffix}`, ['-run', '^TestBillingCompatibility', './internal/store']), 'BILLING_PARITY '),
    };
  });
  if (before.length < 20 || before.length !== after.length) throw new Error(`Incomplete matrix: ${before.length}/${after.length}`);
  for (let index = 0; index < before.length; index++) {
    if (['scenario', 'checkpoint', 'year'].some(key => before[index][key] !== after[index][key])) {
      throw new Error(`Matrix identity mismatch at checkpoint ${index}`);
    }
  }
  await run('docker', ['exec', dbName, 'createdb', '-U', 'treckrr_test', 'treckrr_parity_upgrade'], 'upgrade-database');
  const upgradeBefore = records(await test(images[0], 'upgrade-before', ['-run', '^TestBillingUpgradeParityIntegration$', './internal/store'], 'seed'), 'BILLING_UPGRADE ');
  const upgradeAfter = records(await test(images[1], 'upgrade-after', ['-run', '^TestBillingUpgradeParityIntegration$', './internal/store'], 'verify'), 'BILLING_UPGRADE ');
  if (upgradeBefore.length !== 1 || upgradeAfter.length !== 1) throw new Error('Incomplete same-database upgrade comparison');
  const report = { metadata, before, after, matrixUTCDate, matrixAttempts, upgradeBefore, upgradeAfter,
    ...compareBillingParity({ before, after, upgradeBefore, upgradeAfter }),
    regressionRuns: { migration: false, runtime: false },
  };
  const saveReport = () => fs.writeFileSync(path.join(reportDir, 'comparison.json'), JSON.stringify(report, null, 2));
  saveReport();
  if (report.unexpectedDifferences.matrix.length || report.unexpectedDifferences.upgrade.length) {
    throw new Error('Billing parity failed; inspect comparison.json for unexpected financial or display changes');
  }
  await test(images[1], 'migration-regressions', ['-run', '^TestInvoice(Source|PaymentTerm)Migration', './internal/db']);
  report.regressionRuns.migration = true;
  saveReport();
  if (runtimeRegressions === '1') {
    await test(images[1], 'runtime-regressions', ['./internal/store', './internal/server', './internal/web']);
    report.regressionRuns.runtime = true;
  } else {
    console.log('Full runtime regression suite explicitly skipped; CI must run it in its separate required job.');
  }
  saveReport();
  console.log(`Compared ${before.length} matrix snapshots and the same database before/after migration.`);
  console.log(`Observed field differences: matrix=${report.matrixDifferences.length}, upgrade=${report.upgradeDifferences.length}.`);
  console.log('Financial parity and the explicit reminder/checklist contract passed.');
} finally {
  const cleanup = { cleanup: true };
  const cleanupFailed = error => {
    console.error(error.message);
    process.exitCode = 1;
  };
  for (const name of activeTests) await run('docker', ['rm', '-f', name], `cleanup-${name}`, cleanup).catch(cleanupFailed);
  if (databaseCreated) await run('docker', ['rm', '-f', dbName], 'cleanup-database', cleanup).catch(cleanupFailed);
  if (cacheCreated) await run('docker', ['volume', 'rm', cacheName], 'cleanup-cache', cleanup).catch(cleanupFailed);
  for (const image of images) await run('docker', ['image', 'rm', image], `cleanup-${image.replace(':', '-')}`, cleanup).catch(cleanupFailed);
  process.removeListener('SIGINT', interrupt);
  process.removeListener('SIGTERM', interrupt);
}
