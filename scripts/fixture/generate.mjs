// Generates testdata/teams-fixture with Microsoft Edge driven by Playwright.
// Synthetic data only. Run via `make fixture`.
import { chromium } from 'playwright';
import { cp, mkdtemp, readFile, readdir, rm, mkdir, writeFile, stat } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '../..');
const outRoot = path.join(root, 'testdata/teams-fixture');
const idbRel = 'EBWebView/WV2Profile_fixture/IndexedDB';
const origin = 'https_teams.microsoft.com_0.indexeddb';
const PAGE_URL = 'https://teams.microsoft.com/fixture';

const accounts = [
  { tenant: '00000000-0000-4000-8000-000000000001', user: '00000000-0000-4000-8000-0000000000a1', name: 'Alex Fixture', seed: 1, big: true },
  { tenant: '00000000-0000-4000-8000-000000000002', user: '00000000-0000-4000-8000-0000000000a2', name: 'Blair Fixture', seed: 2, big: false },
];

function fail(msg) { console.error('FIXTURE GENERATION FAILED: ' + msg); process.exit(1); }

const tmp = await mkdtemp(path.join(os.tmpdir(), 'm365crawl-fixture-'));
let version = '';
try {
  const ctx = await chromium.launchPersistentContext(path.join(tmp, 'profile'), { channel: 'msedge', headless: true });
  version = ctx.browser()?.version() ?? '';
  const html = await readFile(path.join(here, 'page.html'), 'utf8');
  await ctx.route('https://teams.microsoft.com/**', (route) => route.fulfill({ status: 200, contentType: 'text/html', body: html }));
  const page = await ctx.newPage();
  await page.goto(PAGE_URL);
  if (!(await page.evaluate(() => isSecureContext))) fail('page is not a secure context');
  for (const a of accounts) await page.evaluate((acct) => window.buildAccount(acct), a);
  await ctx.close(); // flushes the LevelDB log

  // Locate the origin directory the browser wrote.
  const found = [];
  async function walk(d) {
    for (const e of await readdir(d, { withFileTypes: true })) {
      const p = path.join(d, e.name);
      if (e.isDirectory()) { if (e.name.endsWith('.indexeddb.leveldb')) found.push(p); else await walk(p); }
    }
  }
  await walk(path.join(tmp, 'profile'));
  if (found.length !== 1 || path.basename(found[0]) !== origin + '.leveldb') {
    fail('expected exactly ' + origin + '.leveldb, found ' + JSON.stringify(found.map((f) => path.basename(f))));
  }
  const srcIdb = path.dirname(found[0]);
  const dstIdb = path.join(outRoot, idbRel);
  await rm(path.join(outRoot, 'EBWebView'), { recursive: true, force: true }); // keep expected/ (goldens)
  await mkdir(dstIdb, { recursive: true });
  await cp(found[0], path.join(dstIdb, origin + '.leveldb'), { recursive: true });
  const blobSrc = path.join(srcIdb, origin + '.blob');
  if (!existsSync(blobSrc)) fail('no blob directory written: the large value did not spill (raise sizes)');
  await cp(blobSrc, path.join(dstIdb, origin + '.blob'), { recursive: true });
  // Chromium's LOCK file is empty and harmless; keep the tree as written.

  // Chromium's LOG records the temporary profile path; it is informational, so blank it.
  await writeFile(path.join(dstIdb, origin + '.leveldb', 'LOG'), '');

  // The Go inspector asserts every envelope kind exists and reports wire versions.
  const gocmd = process.env.GO || 'go';
  const r = spawnSync(gocmd, ['run', './scripts/fixture/provenance', dstIdb], { cwd: root, encoding: 'utf8', env: { ...process.env, GOWORK: 'off', CGO_ENABLED: '0' } });
  if (r.status !== 0) fail(r.stderr || 'provenance inspector failed');
  const seen = JSON.parse(r.stdout);
  if (!seen.blink_versions.length) fail('no Blink version found');
  const prov = {
    browser: 'msedge',
    browser_version: version,
    blink_versions: seen.blink_versions,
    v8_versions: seen.v8_versions,
    envelope_counts: seen.envelope_counts,
    generated_at: new Date().toISOString(),
  };
  await writeFile(path.join(outRoot, 'PROVENANCE.json'), JSON.stringify(prov, null, 2) + '\n');
} finally {
  await rm(tmp, { recursive: true, force: true });
}
console.log('fixture written to ' + outRoot);
