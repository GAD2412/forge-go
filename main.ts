// Entry point: start the forge binary (bin/forge-<platform>-<arch>, built from src/ by build.sh),
// read the port it bound, register it with the suite, then poll /done and exit 0 when it says so.

import { spawn } from 'node:child_process';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';

const ENV_URL = (process.env.MAKEFUN_ENV_URL ?? '').replace(/\/+$/, '');
const bin = fileURLToPath(new URL(`./bin/forge-${process.platform}-${process.arch}`, import.meta.url));
const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

const child = spawn(bin, [], { stdio: ['ignore', 'pipe', 'inherit'] });
child.on('exit', (code) => process.exit(code ?? 1));
for (const sig of ['SIGTERM', 'SIGINT'] as const) process.on(sig, () => child.kill(sig));

const port: number = await new Promise((resolve) => {
  createInterface({ input: child.stdout! }).on('line', (line) => {
    const m = /^PORT (\d+)$/.exec(line);
    if (m) resolve(Number(m[1]));
  });
});
process.stdout.write(`[entry] forge listening on port ${port}\n`);

if (ENV_URL !== '') {
  for (let attempt = 0; ; attempt += 1) {
    try {
      const res = await fetch(`${ENV_URL}/register`, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ port }),
        signal: AbortSignal.timeout(10_000),
      });
      if (res.status === 200 || res.status === 409) break;
    } catch {
      // the env may still be starting
    }
    await sleep(Math.min(1_000, 50 * 2 ** attempt));
  }
  for (;;) {
    await sleep(1_000);
    try {
      const res = await fetch(`${ENV_URL}/done`, { signal: AbortSignal.timeout(10_000) });
      if (res.ok && ((await res.json()) as { done?: unknown }).done === true) break;
    } catch {
      // not done yet
    }
  }
  child.kill('SIGTERM');
  process.exit(0);
}
