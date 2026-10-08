// scrub makes `m365crawl doctor` text safe to publish, and fails when it cannot be sure.
//
// It rewrites what is known to identify the user:
//   - the home directory becomes "~";
//   - each Outlook profile name (a directory name the user chose) becomes "Main" when there is one
//     profile, else "Profile 1", "Profile 2" and so on, in sorted order;
//   - the names of non-Teams origins in teams_origin become a count.
// Then it fails if the text still holds the user name, the full name or one of its parts (any
// case, as a whole word), a deny-list entry (any case, anywhere), the home or a temp directory,
// an "@", "http", an absolute path that is not under "~", or a profile name it did not rewrite.

const ANSI = /\x1b\[[0-9;]*m/g;
const strip = (s) => s.replace(ANSI, '');
const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

// profileLabels maps each profile name to its neutral label.
export function profileLabels(names) {
  const sorted = [...new Set(names)].sort();
  return new Map(sorted.map((n, i) => [n, sorted.length === 1 ? 'Main' : `Profile ${i + 1}`]));
}

// Words m365crawl prints on its own (its tagline, the Teams and Outlook paths, check names). A
// name equal to one of them cannot identify anyone in this text, and checking it would fail every
// run on a machine account named, say, "microsoft". A deny-list entry still catches it.
const PRODUCT_WORDS = new Set(['microsoft', 'teams', 'outlook', 'office', 'm365crawl']);

// nameTerms is the user name, the full name and the full name's parts of three letters or more,
// lower case, without the product words.
export function nameTerms(user, fullName) {
  const full = (fullName || '').trim().toLowerCase();
  const parts = full.split(/\s+/).filter((p) => p.length >= 3);
  return [...new Set([(user || '').toLowerCase(), full, ...parts].filter((t) => t.length >= 3 && !PRODUCT_WORDS.has(t)))];
}

// A word boundary that treats letters, digits, ".", "_" and "-" as part of a word, so "jane" is
// found in "by jane" and "/Users/jane" but not in "janet" or "com.jane.app".
const asWord = (t) => new RegExp(`(^|[^\\p{L}\\p{N}._-])${esc(t)}($|[^\\p{L}\\p{N}._-])`, 'iu');

export function scrub(text, { homes = [], user = '', fullName = '', deny = [], profiles = [], tmpdirs = [] }) {
  for (const h of [...new Set(homes)].filter((h) => h && h.length > 1).sort((a, b) => b.length - a.length)) {
    text = text.split(h).join('~');
  }
  text = text.replace(/; ignoring non-Teams origins: ([^\n\x1b]*)/g, (_, list) => `; ignoring ${list.split(', ').length} non-Teams origin(s)`);

  const labels = profileLabels(profiles);
  // Longest names first, so "Acme Corp" is not rewritten as "Acme" followed by " Corp".
  for (const name of [...labels.keys()].sort((a, b) => b.length - a.length)) {
    const label = labels.get(name);
    text = text.replace(new RegExp(`(\\bprofile )${esc(name)}(?=:| cannot be examined)`, 'g'), `$1${label}`);
    text = text.replace(new RegExp(`/${esc(name)}(?=$|[/\\s.,;:)])`, 'gm'), `/${label}`);
  }

  const plain = strip(text);
  const problems = [];
  const neutral = new Set(labels.values());
  for (const m of plain.matchAll(/\bprofile (.+?)(?=: | cannot be examined)/g)) {
    if (!neutral.has(m[1])) problems.push(`profile "${m[1]}"`);
  }
  for (const t of nameTerms(user, fullName)) if (asWord(t).test(plain)) problems.push('the user\'s name');
  for (const d of deny.map((d) => d.trim().toLowerCase()).filter(Boolean)) if (plain.toLowerCase().includes(d)) problems.push('a deny-list entry');
  for (const name of labels.keys()) if (!neutral.has(name) && name.length >= 3 && asWord(name.toLowerCase()).test(plain)) problems.push('a profile name');
  for (const p of [...homes, ...tmpdirs]) if (p && p.length > 3 && plain.includes(p)) problems.push(p);
  plain.split('\n').forEach((l, i) => {
    if (l.includes('@')) problems.push(`"@" on line ${i + 1}`);
    if (/http/i.test(l)) problems.push(`"http" on line ${i + 1}`);
    if (/(^|[\s"'(=,:])(\/[^\s/]|[A-Za-z]:\\)/.test(l)) problems.push(`an absolute path outside ~ on line ${i + 1}`);
  });
  if (problems.length) throw new Error('the output identifies the user: ' + [...new Set(problems)].join(', '));
  return text;
}
