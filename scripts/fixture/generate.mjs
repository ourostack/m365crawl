// Generates testdata/teams-fixture with Microsoft Edge driven by Playwright.
// Synthetic data only. Run via `make fixture`.
import { chromium } from 'playwright';
import { cp, mkdtemp, rm, mkdir, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { buildTeamsProfile, origin } from './teams-store.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, '../..');
const outRoot = path.join(root, 'testdata/teams-fixture');
const idbRel = 'EBWebView/WV2Profile_fixture/IndexedDB';

function fail(msg) { console.error('FIXTURE GENERATION FAILED: ' + msg); process.exit(1); }

const tmp = await mkdtemp(path.join(os.tmpdir(), 'm365crawl-fixture-'));
let version = '';
try {
  let built;
  try {
    built = await buildTeamsProfile(chromium, path.join(tmp, 'profile'));
  } catch (e) {
    fail(e.message);
  }
  version = built.version;
  const found = [built.leveldb];
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
