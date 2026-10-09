// Generates synthetic OWA fixtures with the existing Playwright/headless Edge toolchain.
import { chromium } from 'playwright';
import { cp, mkdtemp, mkdir, readdir, rm, rmdir, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { buildOutlookProfile, origin } from './outlook-store.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const out = path.join(root, 'testdata', 'outlook-owa-fixture');
if (existsSync(out)) throw new Error('fixture output already exists; refusing to replace it');
const tmp = await mkdtemp(path.join(os.tmpdir(), 'm365crawl-task4-owa-'));
const disposition = { temporary_profile_root: tmp, profiles: [], removed: false };
try {
  const versions = [];
  for (const variant of ['mail', 'unknown', 'multiple', 'missing-store', 'empty']) {
    const profile = path.join(tmp, variant);
    disposition.profiles.push(profile);
    const built = await buildOutlookProfile(chromium, profile, variant);
    versions.push(built.version);
    const dest = path.join(out, variant, 'IndexedDB');
    await mkdir(dest, { recursive: true });
    await cp(built.leveldb, path.join(dest, origin + '.leveldb'), { recursive: true });
    const blobs = path.join(path.dirname(built.leveldb), origin + '.blob');
    if (variant === 'mail' && !existsSync(blobs)) throw new Error('synthetic large body did not spill to blob');
    if (existsSync(blobs)) await cp(blobs, path.join(dest, origin + '.blob'), { recursive: true });
    for (const e of await readdir(path.join(dest, origin + '.leveldb'))) {
      if (e === 'LOG' || e === 'LOG.old') await writeFile(path.join(dest, origin + '.leveldb', e), '');
    }
  }
  await writeFile(path.join(out, 'PROVENANCE.json'), JSON.stringify({ browser: 'msedge', browser_versions: versions, generated_at: new Date().toISOString(), origin: 'https://outlook.office.com', intercepted: true, synthetic_only: true, truncated_body: 'IsTruncated=true retains available cached HTML inline; not server completeness', scenarios: ['mail', 'unknown', 'multiple', 'missing-store', 'empty'] }, null, 2) + '\n');
} finally {
  // Delete only the exact fresh profile directories created by this invocation, then their empty parent.
  for (const profile of disposition.profiles) await rm(profile, { recursive: true });
  await rmdir(tmp);
  disposition.removed = true;
  console.log(JSON.stringify(disposition));
}
console.log('Synthetic OWA fixture written.');
