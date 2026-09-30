// Converts the SGR subset teamscrawl emits (reset, bold, dim, 16-color foregrounds,
// 38;5;n and 38;2;r;g;b) to HTML spans.
const PALETTE = { 31: '#f7768e', 32: '#9ece6a', 33: '#e0af68', 36: '#7dcfff' };

const escapeHtml = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

export function ansiToHtml(input) {
  let state = { color: null, bold: false, dim: false };
  let out = '';
  let open = false;
  const close = () => { if (open) { out += '</span>'; open = false; } };
  const apply = (codes) => {
    for (let i = 0; i < codes.length; i++) {
      const c = codes[i];
      if (c === 0) state = { color: null, bold: false, dim: false };
      else if (c === 1) state.bold = true;
      else if (c === 2) state.dim = true;
      else if (PALETTE[c]) state.color = PALETTE[c];
      else if (c === 38 && codes[i + 1] === 2) { state.color = `rgb(${codes[i + 2]},${codes[i + 3]},${codes[i + 4]})`; i += 4; }
      else if (c === 38 && codes[i + 1] === 5) { state.color = `var(--c256-${codes[i + 2]}, #8b8dc9)`; i += 2; }
    }
  };
  const re = /\x1b\[([0-9;]*)m/g;
  let last = 0;
  let m;
  while ((m = re.exec(input)) !== null) {
    out += escapeHtml(input.slice(last, m.index));
    last = re.lastIndex;
    close();
    apply(m[1] === '' ? [0] : m[1].split(';').map(Number));
    const style = [];
    if (state.color) style.push(`color:${state.color}`);
    if (state.bold) style.push('font-weight:700');
    if (state.dim) style.push('opacity:.6');
    if (style.length) { out += `<span style="${style.join(';')}">`; open = true; }
  }
  out += escapeHtml(input.slice(last));
  close();
  return out;
}
