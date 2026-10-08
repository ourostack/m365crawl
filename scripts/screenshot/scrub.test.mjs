// Tests of scrub.mjs. Run with `node --test scripts/screenshot/scrub.test.mjs`; make screenshot runs them first.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { scrub, profileLabels, nameTerms } from './scrub.mjs';

const home = '/Users/jdoe';
const base = { homes: [home], user: 'jdoe', fullName: 'Jane Q Doe', deny: [], profiles: [], tmpdirs: ['/var/folders/xy/T'] };
const opts = (o = {}) => ({ ...base, ...o });

test('home becomes ~ and clean text passes', () => {
  const out = scrub('● database_writable  /Users/jdoe/.m365crawl/m365crawl.db is writable\n', opts());
  assert.equal(out, '● database_writable  ~/.m365crawl/m365crawl.db is writable\n');
});

test('ANSI codes are kept', () => {
  assert.equal(scrub('\x1b[32m●\x1b[0m ok\n', opts()), '\x1b[32m●\x1b[0m ok\n');
});

test('one profile is named Main, several are numbered, in sorted order', () => {
  assert.deepEqual(profileLabels(['Acme Corp']), new Map([['Acme Corp', 'Main']]));
  assert.deepEqual(profileLabels(['Zed', 'Acme', 'Acme 2']), new Map([['Acme', 'Profile 1'], ['Acme 2', 'Profile 2'], ['Zed', 'Profile 3']]));
});

test('profile names are replaced in details and paths', () => {
  const text = '● outlook_store  profile Acme Corp: store version readable; profile Acme: last read 0s ago\n'
    + '▲ x  profile Acme cannot be examined (denied)\n'
    + '     fix: Give m365crawl access to the profile directory /Users/jdoe/Library/Group Containers/O/Acme.\n';
  const out = scrub(text, opts({ profiles: ['Acme', 'Acme Corp'] }));
  assert.equal(out, '● outlook_store  profile Profile 2: store version readable; profile Profile 1: last read 0s ago\n'
    + '▲ x  profile Profile 1 cannot be examined (denied)\n'
    + '     fix: Give m365crawl access to the profile directory ~/Library/Group Containers/O/Profile 1.\n');
});

test('a profile the listing missed fails', () => {
  assert.throws(() => scrub('● mail_readable  profile Secret Co: mail read 0s ago\n', opts({ profiles: ['Main'] })), /profile "Secret Co"/);
});

test('words like "Outlook profile;" are not a profile name', () => {
  assert.equal(scrub('● mail_readable  no new Outlook profile; no mail to read\n', opts()), '● mail_readable  no new Outlook profile; no mail to read\n');
});

test('non-Teams origins become a count', () => {
  const out = scrub('● teams_origin  1 Teams origin(s) found; ignoring non-Teams origins: https_intranet.acme.example_0, https_x_0\n', opts());
  assert.equal(out, '● teams_origin  1 Teams origin(s) found; ignoring 2 non-Teams origin(s)\n');
});

test('the user name, the full name and its parts fail in any case', () => {
  for (const t of ['owner JDOE here', 'by jane', 'Doe', 'jane q doe']) {
    assert.throws(() => scrub(t + '\n', opts()), /identifies the user/, t);
  }
  // Inside a longer word it is not the name.
  assert.equal(scrub('janet does\n', opts()), 'janet does\n');
});

test('nameTerms keeps parts of three letters or more', () => {
  assert.deepEqual(nameTerms('jdoe', 'Jane Q Doe'), ['jdoe', 'jane q doe', 'jane', 'doe']);
  assert.deepEqual(nameTerms('', ''), []);
});

test('deny-list entries fail anywhere, in any case', () => {
  assert.throws(() => scrub('hosted at contosoinc\n', opts({ deny: ['ContosoInc'] })), /identifies the user/);
  assert.equal(scrub('fine\n', opts({ deny: ['', ' '] })), 'fine\n');
});

test('@, http, temp dirs and absolute paths outside ~ fail', () => {
  for (const t of ['mail x@y', 'see HTTP://x', 'at /var/folders/xy/T/a', 'archive /opt/data/m.db', 'db C:\\Users\\x', '(/etc/x)']) {
    assert.throws(() => scrub(t + '\n', opts()), /identifies the user/, t);
  }
  assert.equal(scrub('~/Library/x and 2026-10-06..2026-10-22 and a/b\n', opts()), '~/Library/x and 2026-10-06..2026-10-22 and a/b\n');
});

test('a name that is one of the product words m365crawl prints is not a leak', () => {
  // A machine account named "microsoft" must not fail on "local-first Microsoft 365 mirror".
  const o = opts({ user: 'microsoft', fullName: 'Microsoft' });
  assert.equal(scrub('local-first Microsoft 365 mirror\n', o), 'local-first Microsoft 365 mirror\n');
  assert.deepEqual(nameTerms('teams', 'Outlook Office Pat'), ['outlook office pat', 'pat']);
  // A deny-list entry still fails it.
  assert.throws(() => scrub('local-first Microsoft 365 mirror\n', { ...o, deny: ['microsoft'] }), /identifies the user/);
});
