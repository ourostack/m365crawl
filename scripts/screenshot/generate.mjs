// Regenerates screenshot.png: builds m365crawl, syncs the committed fixture into a throwaway
// archive, renders `m365crawl doctor --format text` in color, and screenshots it in a framed
// dark terminal window with Playwright and Microsoft Edge. Run through `make screenshot`.
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';
import { ansiToHtml } from './ansi2html.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
const { chromium } = createRequire(path.join(repo, 'scripts/fixture/package.json'))('playwright');
const go = process.env.GO || 'go';

// A neutral home: the fixture and the archive live under it, and the output shows it as "~",
// so no real user name or temp path reaches the image.
const base = fs.mkdtempSync(path.join(os.tmpdir(), 'm365crawl-shot-'));
const bin = path.join(base, 'm365crawl');
const root = path.join(base, 'Teams', 'EBWebView');
const db = path.join(base, '.m365crawl', 'm365crawl.db');
try {
  execFileSync(go, ['build', '-trimpath', '-o', bin, './cmd/m365crawl'], { cwd: repo, stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '0' } });
  fs.cpSync(path.join(repo, 'testdata/teams-fixture/EBWebView'), root, { recursive: true });
  const env = { ...process.env, HOME: base, CLICOLOR_FORCE: '1', COLORTERM: 'truecolor', COLUMNS: '100', NO_COLOR: '' };
  delete env.NO_COLOR;
  const run = (args, okCodes = [0]) => {
    try {
      return execFileSync(bin, ['--db', db, '--teams-root', root, ...args], { env, encoding: 'utf8' });
    } catch (e) {
      if (okCodes.includes(e.status)) return e.stdout;
      throw e;
    }
  };
  run(['--format', 'json', 'sync']);
  let text = run(['--format', 'text', 'doctor']);
  text = text.split(base).join('~');
  const leaks = [os.userInfo().username, os.homedir(), os.tmpdir()].filter((s) => s && s.length > 3 && s !== '/');
  for (const l of leaks) if (text.includes(l)) throw new Error(`output still contains ${l}`);

  const lines = text.split('\n');
  const markLines = lines.slice(0, 5).join('\n');
  const rest = lines.slice(5).join('\n');
  const html = `<!doctype html><meta charset="utf-8"><style>
  html,body{margin:0;background:transparent}
  body{padding:24px;display:inline-block}
  .win{width:801px;box-sizing:border-box;background:#1a1b26;border-radius:12px;box-shadow:0 12px 40px rgba(0,0,0,.45);overflow:hidden;border:1px solid #2f3146}
  .bar{height:36px;display:flex;align-items:center;gap:8px;padding:0 14px;background:#24283b}
  .dot{width:12px;height:12px;border-radius:50%}
  pre.mark{padding-bottom:4px;line-height:1}
  pre{margin:0;padding:18px 22px 22px;color:#c0caf5;font:13px/1.35 "SF Mono",Menlo,"DejaVu Sans Mono",monospace;white-space:pre}
  </style><div class="win"><div class="bar"><i class="dot" style="background:#ff5f56"></i><i class="dot" style="background:#ffbd2e"></i><i class="dot" style="background:#27c93f"></i></div><pre class="mark">${ansiToHtml(markLines)}</pre><pre style="padding-top:0">${ansiToHtml(rest)}</pre></div>`;
  const page = path.join(base, 'shot.html');
  fs.writeFileSync(page, html);

  const browser = await chromium.launch({ channel: 'msedge', headless: true });
  const ctx = await browser.newContext({ deviceScaleFactor: 2, viewport: { width: 900, height: 900 } });
  const p = await ctx.newPage();
  await p.goto('file://' + page);
  await p.locator('.win').screenshot({ path: path.join(repo, 'screenshot.png'), omitBackground: true });
  await browser.close();
  console.log('wrote screenshot.png');
} finally {
  fs.rmSync(base, { recursive: true, force: true });
}
