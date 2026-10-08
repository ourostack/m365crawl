async ({base, transcriptId, maxBytes, loginHosts}) => {
  try {
    let m;
    try {
      m = await fetch(base + "?select=media/transcripts&$expand=media/transcripts",
                      {headers: {accept: "application/json"}, credentials: "include", cache: "no-store"});
    } catch (e) {
      return {state: "signin_probe"};
    }
    if (m.redirected && loginHosts.includes(new URL(m.url).host)) return {state: "signin", status: m.status};
    if (m.status === 401) return {state: "signin", status: m.status};
    if (m.status === 403) return {state: "no_access", status: m.status};
    if (m.status === 404) return {state: "not_found", status: m.status};
    if (!m.ok) return {state: "failed", status: m.status};
    if (!(m.headers.get("content-type") || "").includes("json")) return {state: "signin", status: m.status};
    const ts = ((await m.json()).media || {}).transcripts || [];
    const tr = ts.find(t => t.id === transcriptId) || (ts.length === 1 ? ts[0] : null);
    if (!tr || !tr.temporaryDownloadUrl) return {state: "no_transcript", status: m.status};
    const u = tr.temporaryDownloadUrl + (tr.temporaryDownloadUrl.includes("?") ? "&" : "?") + "format=json";
    const r = await fetch(u, {cache: "no-store"});
    if (!r.ok) return {state: r.status === 403 ? "no_access" : r.status === 404 ? "not_found" : "failed", status: r.status};
    const buf = await r.arrayBuffer();
    if (buf.byteLength > maxBytes) return {state: "too_large", status: r.status, bytes: buf.byteLength};
    const es = (JSON.parse(new TextDecoder().decode(buf)).entries || []).map(x => ({s: x.speakerDisplayName || "", b: x.startOffset || "", e: x.endOffset || "", t: x.text || ""}));
    return {state: "ok", status: r.status, entries: es};
  } catch (e) {
    return {state: "failed", error: (e && e.name) || "Error"};
  }
}
