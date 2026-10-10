(state) => {
  const utf8 = new TextEncoder();
  const loss = code => state.losses.set(code, (state.losses.get(code) || 0) + 1);
  const fail = code => { state.fatal ||= code; };
  const textSize = value => {
    if (typeof value !== "string") return 0;
    for (let i = 0; i < value.length; i++) {
      const c = value.charCodeAt(i);
      if (c === 0) { fail("malformed"); return 0; }
      if (c >= 0xd800 && c <= 0xdbff) {
        const next = value.charCodeAt(++i);
        if (!(next >= 0xdc00 && next <= 0xdfff)) { fail("malformed"); return 0; }
      } else if (c >= 0xdc00 && c <= 0xdfff) { fail("malformed"); return 0; }
    }
    const bytes = utf8.encode(value).byteLength;
    if (bytes > 262144) fail("too_large");
    return bytes;
  };
  const optional = value => {
    if (value === undefined) return null;
    if (value === null || ["string", "number", "boolean"].includes(typeof value)) return value;
    return {unmapped:true};
  };
  const required = value => typeof value === "string" && value.length > 0;
  const walk = (node, depth, visitThreads) => {
    if (state.fatal) return;
    if (++state.nodes > 65536 || depth > 128) { fail("too_large"); return; }
    if (!node || typeof node !== "object") return;
    if (visitThreads && node.__typename === "Thread") thread(node);
    if (Array.isArray(node)) {
      for (const child of node) walk(child,depth+1,visitThreads);
    } else {
      for (const key of Object.keys(node)) walk(node[key],depth+1,visitThreads);
    }
  };
  const thread = node => {
    const starter = node.threadStarter;
    const content = starter?.languageSpecificContent;
    const body = content?.body;
    if (![node.id,node.network?.id,node.group?.id,starter?.id].every(required) ||
        typeof body?.serializedContentState !== "string") {
      loss("thread_fragment_unmapped");
      return;
    }
    if (state.threads.length >= 128) { fail("too_large"); return; }
    let draft;
    try { draft = JSON.parse(body.serializedContentState); } catch { fail("malformed"); return; }
    walk(draft,0,false);
    if (state.fatal) return;
    if (!draft || typeof draft !== "object" || !Array.isArray(draft.blocks) ||
        draft.blocks.some(block => !block || typeof block.text !== "string")) { fail("malformed"); return; }
    if (draft.blocks.length > 4096 || state.blocks + draft.blocks.length > 65536) { fail("too_large"); return; }
    state.blocks += draft.blocks.length;
    const observation = {
      Ordinal:state.threads.length, ID:node.id, NetworkID:node.network.id, GroupID:node.group.id, StarterID:starter.id,
      CreatedRaw:optional(node.createdAt), UpdatedRaw:optional(node.updatedAt),
      StarterCreatedRaw:optional(starter.createdAt), StarterUpdatedRaw:optional(starter.updatedAt),
      SenderID:optional(starter.sender?.id), Language:optional(content.language), Title:optional(content.title),
      Version:optional(starter.version), IsDeleted:optional(starter.isDeleted), IsDraft:optional(starter.isDraft),
      Blocks:[]
    };
    let bytes = 0;
    for (const value of Object.values(observation)) if (typeof value === "string") bytes += textSize(value);
    for (const block of draft.blocks) {
      const n = textSize(block.text);
      state.textBytes += n;
      bytes += n;
      if (state.fatal || state.textBytes > 1048576 || state.stringBytes + bytes > 8388608) { fail("too_large"); return; }
      observation.Blocks.push(block.text);
    }
    if (typeof observation.Title === "string") state.textBytes += textSize(observation.Title);
    state.stringBytes += bytes;
    if (state.fatal || state.textBytes > 1048576 || state.stringBytes > 8388608) { fail("too_large"); return; }
    if (Array.isArray(body.references) && body.references.length) loss("references_unmapped");
    if (draft.entityMap && typeof draft.entityMap === "object" && Object.keys(draft.entityMap).length) loss("formatting_unmapped");
    if (draft.blocks.some(b => (Array.isArray(b.inlineStyleRanges) && b.inlineStyleRanges.length) ||
        (Array.isArray(b.entityRanges) && b.entityRanges.length))) loss("formatting_unmapped");
    if (node.hasAttachments === true) loss("attachments_unmapped");
    if (typeof node.topLevelReplies?.totalCount === "number" && node.topLevelReplies.totalCount > 0) loss("replies_unmapped");
    state.threads.push(observation);
  };
  return json => {
    if (!json || typeof json !== "object" || Array.isArray(json)) { fail("malformed"); return; }
    if (Object.hasOwn(json,"errors")) {
      if (!Array.isArray(json.errors)) { fail("malformed"); return; }
      if (json.errors.length) loss("graphql_error");
    }
    const data = json.data;
    if (!data || typeof data !== "object" || Array.isArray(data)) { fail("malformed"); return; }
    const viewer = data.viewer?.user;
    if (viewer !== undefined && viewer !== null) {
      if (!required(viewer.id) || !required(viewer.network?.id)) {
        const fragment = {Host:"engage.cloud.microsoft",UserID:required(viewer.id) ? viewer.id : "",NetworkID:required(viewer.network?.id) ? viewer.network.id : ""};
        state.stringBytes += textSize(fragment.UserID) + textSize(fragment.NetworkID) + 22;
        if (state.fatal || state.stringBytes > 8388608) { fail("too_large"); return; }
        state.viewerFragments.push(fragment);
        loss("viewer_fragment_unmapped");
      } else {
      textSize(viewer.id); textSize(viewer.network.id);
      if (state.account && (state.account.UserID !== viewer.id || state.account.NetworkID !== viewer.network.id)) {
        fail("identity_drift"); return;
      }
      if (!state.account) {
        state.account = {Host:"engage.cloud.microsoft",UserID:viewer.id,NetworkID:viewer.network.id};
        state.stringBytes += textSize(viewer.id) + textSize(viewer.network.id) + 22;
      }
      state.stringBytes += textSize(viewer.id) + textSize(viewer.network.id) + 22;
      if (state.stringBytes > 8388608) { fail("too_large"); return; }
      state.accountEvidence.push({Host:"engage.cloud.microsoft",UserID:viewer.id,NetworkID:viewer.network.id});
      }
    }
    walk(data,0,true);
  };
}
