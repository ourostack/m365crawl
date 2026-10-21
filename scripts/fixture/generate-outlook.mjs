// Generates synthetic OWA fixtures with the existing Playwright/headless Edge toolchain.
import { chromium } from 'playwright';
import { cp, mkdtemp, mkdir, readdir, rm, rmdir, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { buildOutlookProfile, origin } from './outlook-store.mjs';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');
const args = process.argv.slice(2);
if (args.length > 1 || (args.length === 1 && !['--calendar', '--calendar-empty-mail', '--calendar-local'].includes(args[0]))) throw new Error('usage: generate-outlook.mjs [--calendar|--calendar-empty-mail|--calendar-local]');
const emptyMail = args[0] === '--calendar-empty-mail';
const calendarLocalDates = args[0] === '--calendar-local';
const calendarOnly = args[0] === '--calendar' || emptyMail || calendarLocalDates;
const scenarios = emptyMail ? ['calendar-empty-mail'] : calendarOnly ? ['calendar', 'calendar-empty', 'calendar-damaged'] : ['mail', 'unknown', 'multiple', 'missing-store', 'empty'];
const fixture = emptyMail ? 'outlook-owa-calendar-empty-mail-fixture' : calendarLocalDates ? 'outlook-owa-calendar-local-fixture' : calendarOnly ? 'outlook-owa-calendar-fixture' : 'outlook-owa-fixture';
const out = path.join(root, 'testdata', fixture);
if (existsSync(out)) throw new Error('fixture output already exists; refusing to replace it');
const tmp = await mkdtemp(path.join(os.tmpdir(), 'm365crawl-task4-owa-'));
const disposition = { temporary_profile_root: tmp, profiles: [], removed: false };
try {
  const versions = [];
  for (const variant of scenarios) {
    const profile = path.join(tmp, variant);
    disposition.profiles.push(profile);
    const built = await buildOutlookProfile(chromium, profile, variant, calendarLocalDates);
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
  await writeFile(path.join(out, 'PROVENANCE.json'), JSON.stringify({ browser: 'msedge', browser_versions: versions, generated_at: new Date().toISOString(), origin: 'https://outlook.office.com', intercepted: true, synthetic_only: true, calendar_local_dates: calendarLocalDates, truncated_body: 'IsTruncated=true retains available cached HTML inline; not server completeness', scenarios }, null, 2) + '\n');
} finally {
  // Calendar runs retain their exact synthetic profiles for explicit resource accounting.
  if (!calendarOnly) {
    for (const profile of disposition.profiles) await rm(profile, { recursive: true });
    await rmdir(tmp);
    disposition.removed = true;
  }
  console.log(JSON.stringify(disposition));
}
console.log('Synthetic OWA fixture written.');
