'use strict';
const { spawn } = require('node:child_process');
let input = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', chunk => { input += chunk; });
process.stdin.on('end', () => {
  const spec = JSON.parse(input);
  // The official signed Node must directly parent Codex. App tools authorize
  // their peer's process ancestry; an unsigned intermediary breaks that check.
  const child = spawn(spec.command, spec.args, {
    cwd: spec.cwd, env: process.env, detached: true,
    stdio: ['ignore', process.stderr, process.stderr],
  });
  child.once('spawn', () => process.stdout.write(JSON.stringify({ pid: child.pid }) + '\n'));
  child.once('error', error => { console.error(error.message); process.exitCode = 1; });
  child.once('exit', (code, signal) => { process.exitCode = code ?? (signal ? 1 : 0); });
});
