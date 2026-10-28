(makeHome, target) => state => {
  const home=makeHome(state), utf8=new TextEncoder();
  const fail=code=>{state.fatal ||= code;};
  const loss=code=>state.losses.set(code,(state.losses.get(code)||0)+1);
  const optional=value=>{
    if(value===null||value===undefined) return null;
    if(["string","number","boolean"].includes(typeof value)) return value;
    return {unmapped:true};
  };
  const string=value=>{
    if(typeof value==="string") {
      const size=utf8.encode(value).byteLength;
      state.stringBytes+=size;
      if(size>262144||state.stringBytes>8388608) fail("too_large");
      return value;
    }
    loss("optional_field_unmapped");return null;
  };
  let messages=0,blocks=0,bytes=0;
  const walk=(value,depth)=>{
    if(state.fatal)return;
    if(++state.nodes>65536||depth>128){fail("too_large");return;}
    if(!value||typeof value!=="object")return;
    for(const [key,child]of Object.entries(value)){
      if(/auth|token|credential|secret|cookie|msal|oneauth|key.?store|keyval|session|key.?value|keyring|crypto|encrypt|pkce|bearer|password|refresh|adal|(^|_)(aad|keys|jwt|e2ee)(_|$)/i.test(key))continue;
      walk(child,depth+1);
    }
  };
  const body=(message,required)=>{
    let draft;
    try {draft=JSON.parse(message?.languageSpecificContent?.body?.serializedContentState);}catch{}
    if(!draft||!Array.isArray(draft.blocks)||draft.blocks.some(b=>typeof b?.text!=="string")) {
      if(required)fail("malformed");else loss("replies_unmapped");
      return {Blocks:null,Links:[],body_state:"unmapped"};
    }
    walk(draft,0);
    if(draft.blocks.length>4096||blocks+draft.blocks.length>65536){fail("too_large");return {};}
    blocks+=draft.blocks.length;
    const text=[];
    for(const block of draft.blocks){
      string(block.text);bytes+=utf8.encode(block.text).byteLength;
      if(bytes>1048576){fail("too_large");return {};}
      text.push(block.text);
    }
    const links=[];
    if(draft.blocks.some(block=>(Array.isArray(block.inlineStyleRanges)&&block.inlineStyleRanges.length)||
      (Array.isArray(block.entityRanges)&&block.entityRanges.length)))loss("formatting_unmapped");
    if(draft.entityMap&&typeof draft.entityMap==="object"){
      const entries=Object.entries(draft.entityMap);
      if(entries.length>128){fail("too_large");return {};}
      for(const [,entity]of entries){
        if(entity?.type==="LINK"&&typeof entity.data?.url==="string")links.push({URL:string(entity.data.url)});
        else loss("formatting_unmapped");
      }
    }
    const refs=message.languageSpecificContent.body.references;
    if(Array.isArray(refs)&&refs.length)loss("references_unmapped");
    return {Blocks:text,Links:links,body_state:"observed"};
  };
  const projectMessage=(node,starter,required)=>{
    if(++messages>128){fail("too_large");return {};}
    if(typeof node?.id!=="string"||node.thread?.id!==target.ThreadID||
       (!required&&node.parentMessage?.id!==starter)){fail("identity_drift");return {};}
    let attachments=null;
    const connection=node.attachments;
    if(Array.isArray(connection?.edges)){
      if(connection.edges.length>128){fail("too_large");return {};}
      attachments=[];
      for(const edge of connection.edges){
        const a=edge?.node;
        if(typeof a?.id!=="string"){loss("attachments_unmapped");continue;}
        attachments.push({ID:string(a.id),Title:string(a.title),native_type:string(a.__typename),URL:string(a.url)});
        if(Object.keys(a).some(key=>!["id","title","__typename","url","description","previewImage"].includes(key))||
          a.description!=null||a.previewImage!=null)loss("attachments_unmapped");
      }
      if(connection.totalCount!==connection.edges.length)loss("attachments_unmapped");
    }else loss("attachments_unmapped");
    return {ID:string(node.id),thread_id:string(node.thread.id),parent_id:required?null:string(node.parentMessage?.id),
      Author:{ID:string(node.sender?.id),Name:string(node.sender?.displayName),network_id:string(node.sender?.network?.id),native_type:string(node.sender?.__typename)},
      created_raw:string(node.createdAt),updated_raw:string(node.updatedAt),Attachments:attachments,...body(node,required)};
  };
  return json=>{
    walk(json?.data,0);
    if(state.fatal)return;
    const control={data:{viewer:json?.data?.viewer}};
    if(Object.hasOwn(json||{},"errors"))control.errors=json.errors;
    home(control);
    const node=json?.data?.thread;
    if(!node||state.fatal)return;
    if(node.id!==target.ThreadID||(target.GroupID&&node.group?.id!==target.GroupID)){
      fail("identity_drift");return;
    }
    if(state.threads.length>=128){fail("too_large");return;}
    const starter=node.threadStarter;
    const first=projectMessage(starter,starter?.id,true);
    const selected={Messages:[first],Replies:{edges_observed:0,edges_known:Array.isArray(node.topLevelReplies?.edges),
      total_count:Number.isSafeInteger(node.topLevelReplies?.totalCount)&&node.topLevelReplies.totalCount>=0?node.topLevelReplies.totalCount:null,
      has_next_page:typeof node.topLevelReplies?.pageInfo?.hasNextPage==="boolean"?node.topLevelReplies.pageInfo.hasNextPage:null,
      has_previous_page:typeof node.topLevelReplies?.pageInfo?.hasPreviousPage==="boolean"?node.topLevelReplies.pageInfo.hasPreviousPage:null}};
    if(Array.isArray(node.topLevelReplies?.edges)){
      if(node.topLevelReplies.edges.length>127){fail("too_large");return;}
      selected.Replies.edges_observed=node.topLevelReplies.edges.length;
      for(const edge of node.topLevelReplies.edges)selected.Messages.push(projectMessage(edge?.node,starter?.id,false));
    }else loss("replies_unmapped");
    if(state.fatal)return;
    const content=starter.languageSpecificContent;
    if(typeof content.title==="string"){
      bytes+=utf8.encode(content.title).byteLength;
      if(bytes>1048576){fail("too_large");return;}
    }
    state.threads.push({ID:node.id,NetworkID:node.network?.id,GroupID:node.group?.id,StarterID:starter.id,
      CreatedRaw:optional(node.createdAt),UpdatedRaw:optional(node.updatedAt),StarterCreatedRaw:optional(starter.createdAt),StarterUpdatedRaw:optional(starter.updatedAt),
      SenderID:optional(starter.sender?.id),Language:optional(content.language),Title:optional(content.title),
      Version:starter.version&&typeof starter.version==="object"&&Object.hasOwn(starter.version,state.versionToken)?
        {nativeNumber:starter.version[state.versionToken]}:optional(starter.version),
      IsDeleted:optional(starter.isDeleted),IsDraft:optional(starter.isDraft),Blocks:first.Blocks,Selected:selected});
  };
}
