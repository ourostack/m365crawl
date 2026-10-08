// Builds the synthetic Teams IndexedDB of page.html with headless Microsoft Edge. Used by
// scripts/fixture/generate.mjs (the committed testdata/teams-fixture, shiftDays 0) and by
// scripts/screenshot/generate.mjs (a throwaway copy whose dates are moved to the present).
import { readFile, readdir } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const PAGE_URL = 'https://teams.microsoft.com/fixture';

export const origin = 'https_teams.microsoft.com_0.indexeddb';

export const accounts = [
  { tenant: '00000000-0000-4000-8000-000000000001', user: '00000000-0000-4000-8000-0000000000a1', name: 'Alex Fixture', seed: 1, big: true },
  { tenant: '00000000-0000-4000-8000-000000000002', user: '00000000-0000-4000-8000-0000000000a2', name: 'Blair Fixture', seed: 2, big: false },
];

// buildTeamsProfile writes the accounts into a fresh headless Edge profile at profileDir and
// returns the browser version and the path of the origin's .leveldb directory. shiftDays moves
// every date of the data by whole days.
export async function buildTeamsProfile(chromium, profileDir, { shiftDays = 0 } = {}) {
  const ctx = await chromium.launchPersistentContext(profileDir, { channel: 'msedge', headless: true });
  const version = ctx.browser()?.version() ?? '';
  const html = await readFile(path.join(here, 'page.html'), 'utf8');
  await ctx.route('https://teams.microsoft.com/**', (route) => route.fulfill({ status: 200, contentType: 'text/html', body: html }));
  const page = await ctx.newPage();
  await page.goto(PAGE_URL);
  if (!(await page.evaluate(() => isSecureContext))) throw new Error('page is not a secure context');
  for (const a of accounts) await page.evaluate((acct) => window.buildAccount(acct), { ...a, shiftDays });
  await ctx.close(); // flushes the LevelDB log

  const found = [];
  async function walk(d) {
    for (const e of await readdir(d, { withFileTypes: true })) {
      const p = path.join(d, e.name);
      if (e.isDirectory()) { if (e.name.endsWith('.indexeddb.leveldb')) found.push(p); else await walk(p); }
    }
  }
  await walk(profileDir);
  if (found.length !== 1 || path.basename(found[0]) !== origin + '.leveldb') {
    throw new Error('expected exactly ' + origin + '.leveldb, found ' + JSON.stringify(found.map((f) => path.basename(f))));
  }
  return { version, leveldb: found[0] };
}
