// Regenerates screenshot.png: renders `m365crawl doctor --format text` in color and screenshots it
// in a framed dark terminal window with Playwright and headless Microsoft Edge. Run through
// `make screenshot`.
//
// Fixture mode (the default) builds m365crawl and runs it under a throwaway HOME that holds only
// synthetic data, laid out where m365crawl looks by default:
//   - Teams: the Teams fixture page (scripts/fixture/page.html) built into a fresh Edge profile,
//     with every date moved by whole days so the data reads as current;
//   - Outlook: the showcase store (scripts/hxshowcase): folders, mail and meetings around now.
// It syncs once, then renders `m365crawl doctor`, so every check is green and nothing is stale.
//
// Real mode (M365CRAWL_SHOT_REAL=1) renders `m365crawl doctor` from the m365crawl on PATH, or
// $M365CRAWL_BIN, with the user's own archive and default roots, and passes no --db or root. It
// rewrites the home directory to "~", Outlook profile names to "Main" or "Profile N" and non-Teams
// origin names to a count, then fails if the text still holds the user name, the full name or its
// parts, an entry of M365CRAWL_SHOT_DENY (comma-separated), the home or temp directory, "@",
// "http" or an absolute path outside "~" (scrub.mjs). It prints the text to stdout for review and
// writes the PNG to a new temp file, never over screenshot.png, unless M365CRAWL_SHOT_OUT names
// the output.
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';
import { ansiToHtml } from './ansi2html.mjs';
import { scrub } from './scrub.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const { chromium } = createRequire(path.join(repo, 'scripts/fixture/package.json'))('playwright');
const { buildTeamsProfile, origin } = await import(path.join(repo, 'scripts/fixture/teams-store.mjs'));
const go = process.env.GO || 'go';
const real = process.env.M365CRAWL_SHOT_REAL === '1';

// The commands the window shows, in order. Each renders as a prompt line and its output.
const COMMANDS = [['doctor']];

// The Teams fixture's own "today": its calendar last synced on 2023-11-14 and its chat messages
// follow. Shifting by whole days keeps every weekday and time of day.
const FIXTURE_DAY = Date.UTC(2023, 10, 15);

// Default locations under a home directory (internal/teamsdesktop and internal/outlookdesktop).
const TEAMS_REL = ['Library', 'Containers', 'com.microsoft.teams2', 'Data', 'Library', 'Application Support', 'Microsoft', 'MSTeams', 'EBWebView'];
const OUTLOOK_REL = ['Library', 'Group Containers', 'UBF8T346G9.Office', 'Outlook', 'Outlook 15 Profiles'];

const colorEnv = { CLICOLOR_FORCE: '1', COLORTERM: 'truecolor', COLUMNS: '100' };

function runner(bin, env) {
  return (args, okCodes = [0]) => {
    try {
      return execFileSync(bin, args, { env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
    } catch (e) {
      if (okCodes.includes(e.status)) return e.stdout;
      throw e;
    }
  };
}

// fixtureHome builds the binary and the synthetic data under base and returns a runner.
async function fixtureHome(base) {
  const bin = path.join(base, 'bin', 'm365crawl');
  execFileSync(go, ['build', '-trimpath', '-o', bin, './cmd/m365crawl'], { cwd: repo, stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '0' } });

  const now = new Date();
  const shiftDays = Math.floor((now.getTime() - FIXTURE_DAY) / 86400000);
  const built = await buildTeamsProfile(chromium, path.join(base, 'edge-profile'), { shiftDays });
  const teamsRoot = path.join(base, ...TEAMS_REL);
  const idb = path.join(teamsRoot, 'WV2Profile_fixture', 'IndexedDB');
  fs.mkdirSync(idb, { recursive: true });
  fs.cpSync(built.leveldb, path.join(idb, origin + '.leveldb'), { recursive: true });
  const blob = path.join(path.dirname(built.leveldb), origin + '.blob');
  if (fs.existsSync(blob)) fs.cpSync(blob, path.join(idb, origin + '.blob'), { recursive: true });
  fs.writeFileSync(path.join(idb, origin + '.leveldb', 'LOG'), ''); // it names the Edge profile path
  fs.rmSync(path.join(base, 'edge-profile'), { recursive: true, force: true });

  const outlookRoot = path.join(base, ...OUTLOOK_REL);
  execFileSync(go, ['run', './scripts/hxshowcase', '-out', outlookRoot, '-now', now.toISOString().replace(/\.\d+Z$/, 'Z')],
    { cwd: repo, stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '0' } });

  // Only the throwaway home: no M365CRAWL_* setting of the caller's reaches the run, and the two
  // roots are named explicitly as well as being the defaults under HOME.
  const env = Object.fromEntries(Object.entries(process.env).filter(([k]) => !k.startsWith('M365CRAWL_') && k !== 'NO_COLOR'));
  Object.assign(env, colorEnv, { HOME: base, M365CRAWL_TEAMS_ROOT: teamsRoot, M365CRAWL_OUTLOOK_ROOT: outlookRoot });
  const run = runner(bin, env);
  run(['--format', 'json', 'sync']);
  return { run, home: base, profiles: outlookProfiles(outlookRoot) };
}

// outlookProfiles lists the directory names under the Outlook root this run reads: the profile
// names doctor can print. Only names are read, never a store.
function outlookProfiles(root) {
  if (!root || root === 'none') return [];
  try {
    return fs.readdirSync(root, { withFileTypes: true }).filter((e) => e.isDirectory()).map((e) => e.name);
  } catch {
    return [];
  }
}

// fullName is the account's full name (macOS `id -F`), or empty.
function fullName() {
  try {
    return execFileSync('id', ['-F'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim();
  } catch {
    return '';
  }
}

function realHome() {
  const bin = process.env.M365CRAWL_BIN || 'm365crawl';
  const env = { ...process.env, ...colorEnv };
  delete env.NO_COLOR;
  const home = os.homedir();
  const root = process.env.M365CRAWL_OUTLOOK_ROOT || path.join(home, ...OUTLOOK_REL);
  return { run: runner(bin, env), home, profiles: outlookProfiles(root) };
}

function scrubOptions(home, profiles) {
  const homes = [home, os.homedir()].flatMap((h) => { try { return [h, fs.realpathSync(h)]; } catch { return [h]; } });
  return {
    homes, profiles, user: os.userInfo().username, fullName: fullName(),
    deny: (process.env.M365CRAWL_SHOT_DENY || '').split(','),
    tmpdirs: [os.tmpdir(), fs.realpathSync(os.tmpdir())],
  };
}

const strip = (s) => s.replace(/\x1b\[[0-9;]*m/g, '');

// The column a wrapped line continues at: after the leading spaces and, when the line has a label
// (a check name, a snapshot label), after the label and the run of spaces that follows it.
function hangOf(plain) {
  const m = /^(\s*\S.*?\S? {2,})\S/.exec(plain);
  const lead = /^\s*/.exec(plain)[0].length;
  return m ? m[1].length : lead;
}

function linesHtml(text) {
  return text.split('\n').map((l) => {
    const hang = hangOf(strip(l));
    // A path may break after any "/" (a zero-width space), so it wraps at a directory, not at
    // a space inside one.
    const html = ansiToHtml(l.replace(/(\S)\/(?=\S)/g, '$1/\u200b'));
    return `<div class="l" style="padding-left:${hang}ch;text-indent:-${hang}ch">${html || '&nbsp;'}</div>`;
  }).join('');
}

function panel(argv, text) {
  const lines = text.replace(/\n+$/, '').split('\n');
  // The wordmark is block glyphs that must touch: it gets line height 1 and is never wrapped.
  const mark = lines.findIndex((l) => strip(l).trim() === '' || /[a-z]/.test(strip(l)));
  const head = mark > 0 ? lines.slice(0, mark) : [];
  const rest = mark > 0 ? lines.slice(mark) : lines;
  const prompt = `<div class="prompt"><span class="ps">$</span> <span class="cmd">m365crawl ${argv.join(' ')}</span></div>`;
  return prompt + (head.length ? `<pre class="mark">${ansiToHtml(head.join('\n'))}</pre>` : '') + `<div class="out">${linesHtml(rest.join('\n'))}</div>`;
}

const base = fs.mkdtempSync(path.join(os.tmpdir(), 'm365crawl-shot-'));
try {
  const { run, home, profiles } = real ? realHome() : await fixtureHome(base);
  const opts = scrubOptions(home, profiles);
  const panels = [];
  const texts = [];
  for (const argv of COMMANDS) {
    const text = scrub(run(['--format', 'text', ...argv], [0, 3]), opts);
    texts.push(`$ m365crawl ${argv.join(' ')}\n${strip(text)}`);
    panels.push(panel(argv, text));
  }
  if (real) process.stdout.write(texts.join('\n') + '\n');

  const html = `<!doctype html><meta charset="utf-8"><style>
  html,body{margin:0;background:transparent}
  body{padding:24px;display:inline-block}
  .win{width:801px;box-sizing:border-box;background:#1a1b26;border-radius:12px;box-shadow:0 12px 40px rgba(0,0,0,.45);overflow:hidden;border:1px solid #2f3146}
  .bar{height:36px;display:flex;align-items:center;gap:8px;padding:0 14px;background:#24283b}
  .dot{width:12px;height:12px;border-radius:50%}
  .body{padding:16px 20px 22px;color:#c0caf5;font:12.5px/1.4 "SF Mono",Menlo,"DejaVu Sans Mono",monospace}
  .body + .body{padding-top:0}
  .prompt{margin-bottom:10px}
  .ps{color:#9ece6a}
  .cmd{color:#c0caf5;font-weight:700}
  pre{margin:0;font:inherit}
  pre.mark{font-size:13px;line-height:13px;white-space:pre;margin-bottom:6px}
  .l{white-space:pre-wrap;overflow-wrap:anywhere}
  </style><div class="win"><div class="bar"><i class="dot" style="background:#ff5f56"></i><i class="dot" style="background:#ffbd2e"></i><i class="dot" style="background:#27c93f"></i></div>${panels.map((p) => `<div class="body">${p}</div>`).join('')}</div>`;
  const page = path.join(base, 'shot.html');
  fs.writeFileSync(page, html);

  const browser = await chromium.launch({ channel: 'msedge', headless: true });
  try {
    const ctx = await browser.newContext({ deviceScaleFactor: 2, viewport: { width: 900, height: 900 } });
    const p = await ctx.newPage();
    await p.goto('file://' + page);
    // Output lines wrap inside the window by design. What must not happen is a run with no break
    // opportunity (a long path or word) that is wider than the window: the element then scrolls
    // sideways and the screenshot clips it. The wordmark and the prompt never wrap, so they must fit.
    const over = await p.evaluate(() => [...document.querySelectorAll('.l, pre.mark, .prompt')].filter((e) => e.scrollWidth > e.clientWidth + 1).length);
    if (over) throw new Error(`${over} line(s) have text wider than the window that cannot wrap`);
    const out = process.env.M365CRAWL_SHOT_OUT
      || (real ? path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'm365crawl-doctor-')), 'doctor.png') : path.join(repo, 'screenshot.png'));
    await p.locator('.win').screenshot({ path: out, omitBackground: true });
    console.error('wrote ' + (real ? out : path.relative(process.cwd(), out)));
  } finally {
    await browser.close();
  }
} finally {
  fs.rmSync(base, { recursive: true, force: true });
}
