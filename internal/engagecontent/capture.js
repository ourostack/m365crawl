(makeProjection) => {
  if (window !== top || location.protocol !== "https:" || location.hostname !== "engage.cloud.microsoft" ||
      (location.port && location.port !== "443") || window.__m365crawlEngageCapture) return;
  const state = {fatal:"",account:null,accountEvidence:[],viewerFragments:[],threads:[],losses:new Map(),nodes:0,blocks:0,textBytes:0,stringBytes:0};
  const generation = [...crypto.getRandomValues(new Uint32Array(4))].join(":");
  state.versionToken = Symbol("native version token");
  const project = makeProjection(state);
  const original = window.fetch;
  const readers = new Set();
  const tasks = new Set();
  const responses = new Map();
  let stopped = false, nextOrdinal = 0, processed = 0, bytesTotal = 0;
  const fail = code => { state.fatal ||= code; };
  const flush = () => {
    while (responses.has(processed) && !state.fatal) {
      const value = responses.get(processed);
      responses.delete(processed++);
      project(value);
    }
  };
  const read = async (response, ordinal) => {
    let reader;
    try {
      if (!response.ok || !(response.headers.get("content-type") || "").toLowerCase().includes("json")) {
        fail("response_failed"); return;
      }
      if (response.url) {
        const target = new URL(response.url);
        if (target.protocol !== "https:" || target.hostname !== "engage.cloud.microsoft" || target.username ||
            target.password || (target.port && target.port !== "443")) { fail("response_failed"); return; }
      }
      const copy = response.clone();
      if (!copy.body) { fail("malformed"); return; }
      reader = copy.body.getReader();
      readers.add(reader);
      const chunks = [];
      let bytes = 0;
      while (!stopped && !state.fatal) {
        const result = await reader.read();
        if (result.done) break;
        bytes += result.value.byteLength;
        bytesTotal += result.value.byteLength;
        if (bytes > 2097152 || bytesTotal > 8388608) { fail("too_large"); return; }
        chunks.push(result.value);
      }
      if (stopped || state.fatal) return;
      const buffer = new Uint8Array(bytes);
      let offset = 0;
      for (const chunk of chunks) { buffer.set(chunk,offset); offset += chunk.byteLength; }
      const json = JSON.parse(new TextDecoder("utf-8",{fatal:true}).decode(buffer), (key,value,context) => {
        if (key !== "version" || typeof value !== "number") return value;
        return {[state.versionToken]:typeof context?.source === "string" ? context.source : null};
      });
      responses.set(ordinal,json);
      flush();
    } catch { if (!stopped) fail("malformed"); }
    finally {
      if (reader) {
        try { await reader.cancel(); } catch { fail("cleanup_failed"); }
        try { reader.releaseLock(); } catch { fail("cleanup_failed"); }
        readers.delete(reader);
      }
    }
  };
  const wrapper = async (...args) => {
    let selected = false;
    try {
      const input = args[0] instanceof Request ? args[0].url : args[0];
      const url = new URL(input,location.origin);
      const method = String(args[1]?.method || (args[0] instanceof Request ? args[0].method : "GET")).toUpperCase();
      selected = url.protocol === "https:" && url.hostname === "engage.cloud.microsoft" && !url.username && !url.password &&
        (!url.port || url.port === "443") && /\/graphql\/?$/.test(url.pathname) && ["GET","POST"].includes(method);
    } catch {}
    const response = await original(...args);
    if (selected && !stopped && !state.fatal) {
      if (nextOrdinal >= 128) { fail("too_large"); return response; }
      const ordinal = nextOrdinal++;
      const task = read(response,ordinal);
      tasks.add(task);
      void task.finally(() => tasks.delete(task));
    }
    return response;
  };
  window.fetch = wrapper;
  window.__m365crawlEngageCapture = {
    Generation:generation,
    async stop() {
      if (stopped) return {Fatal:"already_stopped"};
      stopped = true;
      if (window.fetch === wrapper) window.fetch = original;
      if (location.protocol !== "https:" || location.hostname !== "engage.cloud.microsoft" ||
          (location.port && location.port !== "443")) fail("document_changed");
      if (readers.size) fail("capture_incomplete");
      for (const reader of readers) {
        try { await reader.cancel(); } catch { fail("cleanup_failed"); }
      }
      await Promise.allSettled([...tasks]);
      responses.clear();
      if (!state.account) fail("identity_missing");
      for (const fragment of state.viewerFragments) {
        if (state.account && ((fragment.UserID && fragment.UserID !== state.account.UserID) ||
            (fragment.NetworkID && fragment.NetworkID !== state.account.NetworkID))) fail("identity_drift");
      }
      if (state.fatal) return {Fatal:state.fatal};
      const threads = state.threads.filter(thread => {
        if (thread.NetworkID === state.account.NetworkID) return true;
        state.losses.set("foreign_network_unmapped",(state.losses.get("foreign_network_unmapped") || 0)+1);
        return false;
      }).map((thread, ordinal) => ({...thread,Ordinal:ordinal}));
      if (!threads.length) state.losses.set("native_content_not_observed",1);
      return {
        State:threads.length ? "observations" : "metadata_only", Generation:generation, Fatal:"", Account:state.account, AccountEvidence:state.accountEvidence, ViewerFragments:state.viewerFragments, Threads:threads,
        Losses:[...state.losses].sort(([a],[b])=>a.localeCompare(b)).map(([Code,Count])=>({Code,Count}))
      };
    }
  };
}
