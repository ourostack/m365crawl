({origin, signal, maxBodyBytes, maxTotalBytes, maxRequests}) => {
  let expected;
  try {
    expected = new URL(origin);
  } catch {
    return async () => ({state: "malformed"});
  }
  if (expected.protocol !== "https:" || expected.username || expected.password ||
      (expected.port && expected.port !== "443") ||
      !Number.isSafeInteger(maxBodyBytes) || maxBodyBytes < 1 || maxBodyBytes > 2097152 ||
      !Number.isSafeInteger(maxTotalBytes) || maxTotalBytes < 1 || maxTotalBytes > 8388608 ||
      !Number.isSafeInteger(maxRequests) || maxRequests < 1 || maxRequests > 8) {
    return async () => ({state: "malformed"});
  }
  let requests = 0, totalBytes = 0;
  const loginHosts = new Set(["login.microsoftonline.com","login.microsoft.com","login.live.com","device.login.microsoftonline.com"]);
  return async reference => {
    if (signal.aborted) return {state: "timeout"};
    let target;
    try {
      target = new URL(reference, expected.origin);
    } catch {
      return {state: "malformed"};
    }
    if (target.origin !== expected.origin || target.username || target.password) return {state: "elsewhere"};
    if (requests >= maxRequests) return {state: "too_large"};
    requests++;
    try {
      const response = await fetch(target.href, {
        method: "GET", credentials: "same-origin", cache: "no-store",
        headers: {accept: "application/json;odata=nometadata"}, signal
      });
      const refuse = async state => {
        if (response.body) await response.body.cancel();
        return {state, status: response.status};
      };
      if (signal.aborted) return await refuse("timeout");
      let final;
      try {
        final = new URL(response.url);
      } catch {
        return await refuse("unexpected_response");
      }
      if (final.username || final.password) return await refuse("elsewhere");
      if (final.origin !== expected.origin) {
        return await refuse(loginHosts.has(final.hostname) ? "signin_required" : "elsewhere");
      }
      if (response.status === 401) return await refuse("signin_required");
      if (response.status === 403) return await refuse("no_access");
      if (response.status === 404) return await refuse("not_found");
      if (!response.ok) return await refuse("failed");
      if (!/^application\/(?:[a-z0-9.+-]+\+)?json(?:\s*;|$)/i.test(response.headers.get("content-type") || "")) {
        return await refuse("unexpected_response");
      }
      if (!response.body) return {state: "malformed", status: response.status};
      const declared = Number(response.headers.get("content-length") || 0);
      if (declared > maxBodyBytes || declared > maxTotalBytes-totalBytes) return await refuse("too_large");
      const reader = response.body.getReader();
      const chunks = [];
      let bytes = 0;
      try {
        for (;;) {
          const {done, value} = await reader.read();
          if (done) break;
          bytes += value.byteLength;
          totalBytes += value.byteLength;
          if (bytes > maxBodyBytes || totalBytes > maxTotalBytes) {
            await reader.cancel();
            return {state: "too_large", status: response.status};
          }
          chunks.push(value);
        }
      } finally {
        reader.releaseLock();
      }
      const raw = new Uint8Array(bytes);
      let offset = 0;
      for (const chunk of chunks) {
        raw.set(chunk, offset);
        offset += chunk.byteLength;
      }
      let json;
      try {
        json = JSON.parse(new TextDecoder("utf-8", {fatal: true}).decode(raw));
      } catch {
        return {state: "malformed", status: response.status};
      }
      if (signal.aborted) return {state: "timeout", status: response.status};
      return {state: "ok", status: response.status, bytes, json};
    } catch {
      return {state: signal.aborted ? "timeout" : "failed"};
    }
  };
}
