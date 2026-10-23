import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { readFile, mkdir } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

// Never accept an existing compose project, database URL, compose file or port.
// Every invocation owns a fresh namespace and can clean up only that namespace.
const root = fileURLToPath(new URL('../', import.meta.url));
const project = `treckrr-e2e-${randomBytes(6).toString('hex')}`;
const reportDir = path.join(root, 'tmp', 'e2e-runs', project, 'playwright-report');
const resultsDir = path.join(root, 'tmp', 'e2e-runs', project, 'test-results');
const compose = ['compose', '--project-name', project, '--project-directory', root,
  '--env-file', path.join(root, 'e2e', 'empty.env'), '-f', path.join(root, 'compose.e2e.yml')];
let child;
let interrupted = false;

async function docker(args, input) {
  return new Promise((resolve, reject) => {
    child = spawn('docker', [...compose, ...args], {
      cwd: root, stdio: [input === undefined ? 'ignore' : 'pipe', 'inherit', 'inherit'],
      env: { ...process.env, TRECKRR_E2E_REPORT_DIR: reportDir, TRECKRR_E2E_RESULTS_DIR: resultsDir },
    });
    child.on('error', reject);
    child.on('close', (code) => code === 0 ? resolve() : reject(new Error(`docker ${args[0]} exited ${code}`)));
    if (input !== undefined) child.stdin.end(input);
  });
}

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.once(signal, () => { interrupted = true; child?.kill('SIGTERM'); });
}

console.log(`Isolated E2E project: ${project}`);
try {
  const packageData = JSON.parse(await readFile(path.join(root, 'e2e', 'package.json'), 'utf8'));
  const dockerfile = await readFile(path.join(root, 'e2e', 'Dockerfile'), 'utf8');
  const browserVersion = dockerfile.match(/playwright:v([\d.]+)-/)?.[1];
  if (browserVersion !== packageData.devDependencies['@playwright/test']) {
    throw new Error('e2e/Dockerfile browser version must exactly match @playwright/test; update both together');
  }
  await Promise.all([reportDir, resultsDir].map((directory) => mkdir(directory, { recursive: true })));
  await docker(['build', 'app', 'browser']);
  if (interrupted) throw new Error('interrupted');
  await docker(['up', '-d', '--wait', '--wait-timeout', '120', 'app']);
  await docker(['port', 'app', '8080']);
  await docker(['exec', '-T', 'db', 'psql', '-U', 'treckrr_e2e', '-d', 'treckrr_e2e', '-v', 'ON_ERROR_STOP=1'],
    await readFile(path.join(root, 'e2e', 'seed.sql')));
  if (interrupted) throw new Error('interrupted');
  // --visual runs only the visual regression projects on this fresh stack.
  const visual = process.argv.includes('--visual');
  const args = process.argv.slice(2).filter((arg) => arg !== '--visual');
  await docker(['run', '--rm', '--no-deps', ...(visual ? ['-e', 'TRECKRR_E2E_VISUAL=1'] : []),
    'browser', 'npx', 'playwright', 'test', ...args]);
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
  await docker(['logs', '--no-color', '--tail', '100', 'app', 'db']).catch(() => {});
} finally {
  // Only this random test namespace; never the standard development compose.
  await docker(['down', '--volumes', '--remove-orphans']).catch((error) => {
    console.error(`Cleanup failed for ${project}: ${error.message}`);
    process.exitCode = 1;
  });
  // Remove only the two image tags generated for this run; shared base images
  // and build cache remain available for subsequent runs.
  await new Promise((resolve) => {
    const cleanup = spawn('docker', ['image', 'rm', `${project}-app`, `${project}-browser`], {
      cwd: root, stdio: ['ignore', 'inherit', 'inherit'],
    });
    cleanup.on('error', resolve);
    cleanup.on('close', resolve);
  });
  console.log(`Report: ${reportDir}`);
  console.log(`Results: ${resultsDir}`);
}
