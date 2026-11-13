import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../../', import.meta.url));
const shell = process.platform === 'win32'
  ? path.join(process.env.ProgramFiles || 'C:/Program Files', 'Git/bin/bash.exe')
  : '/bin/sh';
const gate = 'sh .github/scripts/check-deadcode.sh';
const makefile = readFileSync(new URL('../../Makefile', import.meta.url), 'utf8');
const workflow = readFileSync(new URL('../workflows/deadcode.yml', import.meta.url), 'utf8');
const recipe = makefile.match(/^\t\$\(GO\) "(go install golang\.org\/x\/tools\/cmd\/deadcode@[^\r\n]+)"\r?$/m)?.[1];

// Only these synthetic tools are visible to the child: no real Go installation,
// analyzer, Docker, network or database command can be invoked by the mocks.
function runGate(t, { present = true, output = '', status = 0, installStatus = 0, command = gate } = {}) {
  const fixture = mkdtempSync(path.join(tmpdir(), 'treckrr-deadcode-gate-'));
  t.after(() => rmSync(fixture, { recursive: true, force: true }));
  const bin = path.join(fixture, 'bin');
  mkdirSync(bin);
  const argsLog = path.join(fixture, 'analyzer-args');
  const installLog = path.join(fixture, 'install-args');
  if (present) {
    writeFileSync(path.join(bin, 'deadcode'), `#!/bin/sh
printf '%s\\n' "$@" > "$MOCK_ARGS_LOG"
printf '%s' "$MOCK_OUTPUT"
exit "$MOCK_STATUS"
`, { mode: 0o700 });
  }
  writeFileSync(path.join(bin, 'go'), `#!/bin/sh
printf '%s\\n' "$@" > "$MOCK_INSTALL_LOG"
exit "$MOCK_INSTALL_STATUS"
`, { mode: 0o700 });
  const portable = (value) => value.replaceAll('\\', '/');
  const result = spawnSync(shell, ['-c', `
set -eu
test_bin=$(cd "$1" && pwd)
test_shell=$3
sh() { "$test_shell" "$@"; }
export PATH="$test_bin"
eval "$2"
`, 'deadcode-gate-test', portable(bin), command, portable(shell)], {
    cwd: root,
    encoding: 'utf8',
    timeout: 10_000,
    env: { ...process.env, MOCK_ARGS_LOG: portable(argsLog), MOCK_INSTALL_LOG: portable(installLog),
      MOCK_OUTPUT: output, MOCK_STATUS: String(status), MOCK_INSTALL_STATUS: String(installStatus) },
  });
  assert.ifError(result.error);
  assert.equal(result.signal, null);
  return { ...result, argsLog, installLog };
}

test('a successful analyzer with no findings passes and receives the CI flags', (t) => {
  const result = runGate(t);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout.trim(), 'No dead code found.');
  assert.equal(result.stderr, '');
  assert.deepEqual(readFileSync(result.argsLog, 'utf8').trim().split('\n'),
    ['-test', '-tags=integration', './...']);
});

test('findings fail even when the analyzer returns zero', (t) => {
  const finding = 'internal/store/example.go:1:1: unreachable func: Store.Unused';
  const result = runGate(t, { output: `${finding}\n` });
  assert.equal(result.status, 1);
  assert.match(result.stdout, /Dead code found:/);
  assert.ok(result.stdout.includes(finding));
  assert.doesNotMatch(result.stdout, /No dead code found/);
});

test('analyzer failure without stdout preserves its nonzero exit status', (t) => {
  const result = runGate(t, { status: 42 });
  assert.equal(result.status, 42);
  assert.equal(result.stdout, '');
  assert.match(result.stderr, /Deadcode analysis failed \(exit 42\)/);
});

test('a missing analyzer fails instead of reporting a clean scan', (t) => {
  const result = runGate(t, { present: false });
  assert.equal(result.status, 127);
  assert.equal(result.stdout, '');
  assert.match(result.stderr, /Deadcode analysis failed \(exit 127\)/);
});

test('Makefile installation failures cannot be overwritten by an empty scan', (t) => {
  assert.ok(recipe, 'missing Makefile deadcode command');
  const result = runGate(t, { command: recipe, installStatus: 43 });
  assert.equal(result.status, 43);
  assert.equal(result.stdout, '');
  assert.throws(() => readFileSync(result.argsLog), { code: 'ENOENT' });
  assert.deepEqual(readFileSync(result.installLog, 'utf8').trim().split('\n'),
    ['install', 'golang.org/x/tools/cmd/deadcode@v0.52.0']);
});

test('Makefile uses the same gate and preserves analyzer failures', (t) => {
  assert.ok(recipe, 'missing Makefile deadcode command');
  const result = runGate(t, { command: recipe, status: 44 });
  assert.equal(result.status, 44);
  assert.match(result.stderr, /Deadcode analysis failed \(exit 44\)/);
  assert.match(workflow, /run: sh \.github\/scripts\/check-deadcode\.sh/);
  assert.match(workflow, /run: node --test \.github\/scripts\/check-deadcode\.test\.mjs/);
});
