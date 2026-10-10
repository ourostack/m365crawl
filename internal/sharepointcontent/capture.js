async (args, readerFactory) => {
  if (location.hostname.toLowerCase() !== args.Host) return {state:"elsewhere"};
  const controller=new AbortController();
  const timer=setTimeout(() => controller.abort(),20000);
  const read=readerFactory({origin:location.origin,signal:controller.signal,maxBodyBytes:2097152,maxTotalBytes:8388608,maxRequests:8});
  const uuid=value => typeof value==="string" && /^(?:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|\{[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\})$/i.test(value);
  const normalUUID=value => value.replace(/[{}]/g,"").toLowerCase();
  const string=value => typeof value==="string" && !value.includes("\0") && new TextEncoder().encode(value).byteLength<=262144;
  const optional=value => value===undefined || value===null || string(value);
  const unwrap=result => {
    if (!result.json || typeof result.json!=="object" || Array.isArray(result.json)) return null;
    return Object.hasOwn(result.json,"d") ? result.json.d : result.json;
  };
  try {
    const literal="'"+args.Path.replace(/'/g,"''")+"'";
    const encoded=encodeURIComponent(literal).replace(/'/g,"%27");
    const fileRead=await read(args.Site+"/_api/web/GetFileByServerRelativePath(decodedurl="+encoded+")?$select=UniqueId,ServerRelativeUrl,TimeLastModified,VroomDriveID,VroomItemID,Length,ListItemAllFields&$expand=ListItemAllFields");
    if (fileRead.state!=="ok") return {state:fileRead.state,status:fileRead.status};
    const native=unwrap(fileRead);
    if (!native || !uuid(native.UniqueId) || !string(native.ServerRelativeUrl)) return {state:"malformed"};
    if (native.ServerRelativeUrl!==args.Path) return {state:"identity_mismatch"};
    const siteRead=await read(args.Site+"/_api/site?$select=Id");
    if (siteRead.state!=="ok") return {state:siteRead.state,status:siteRead.status};
    const webRead=await read(args.Site+"/_api/web?$select=Id");
    if (webRead.state!=="ok") return {state:webRead.state,status:webRead.status};
    const accountRead=await read(args.Site+"/_api/web/currentuser?$select=Id,LoginName");
    if (accountRead.state!=="ok") return {state:accountRead.state,status:accountRead.status};
    const site=unwrap(siteRead),web=unwrap(webRead),account=unwrap(accountRead);
    if (!site || !web || !account || !uuid(site.Id) || !uuid(web.Id) ||
        !Number.isSafeInteger(account.Id) || account.Id<=0 || !string(account.LoginName) || !account.LoginName) {
      return {state:"malformed"};
    }
    const item=native.ListItemAllFields;
    if ((item!==undefined && item!==null && (typeof item!=="object" || Array.isArray(item))) ||
        !optional(native.VroomDriveID) || !optional(native.VroomItemID) || !optional(native.TimeLastModified) ||
        !optional(item?.Title) || (item?.Id!==undefined && item?.Id!==null && (!Number.isSafeInteger(item.Id) || item.Id<1))) {
      return {state:"malformed"};
    }
    let length=null;
    if (native.Length!==undefined && native.Length!==null) {
      if (typeof native.Length!=="string" || !/^[0-9]+$/.test(native.Length) || !Number.isSafeInteger(Number(native.Length))) return {state:"malformed"};
      length=Number(native.Length);
    }
    const file={
      SiteID:normalUUID(site.Id),WebID:normalUUID(web.Id),FileID:normalUUID(native.UniqueId),Path:args.Path,
      DriveID:native.VroomDriveID ?? null,ItemID:native.VroomItemID ?? null,Title:item?.Title ?? null,
      ListItemID:item?.Id ?? null,Length:length,ModifiedRaw:native.TimeLastModified ?? null
    };
    const losses=[];
    if (typeof file.ModifiedRaw==="string" && (!/^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2})$/.test(file.ModifiedRaw) || !Number.isFinite(Date.parse(file.ModifiedRaw)))) {
      losses.push({Code:"modified_time_unmapped",Count:1});
    }
    if (controller.signal.aborted) return {state:"timeout"};
    const result={state:"metadata_only",status:200,account:{Host:args.Host,WebID:file.WebID,LoginName:account.LoginName,ID:account.Id},file,losses};
    if (args.Kind==="page" && item?.CanvasContent1!==undefined && item?.CanvasContent1!==null) {
      if (typeof item.CanvasContent1!=="string") return {state:"malformed"};
      const template=document.createElement("template");
      template.innerHTML=item.CanvasContent1;
      let nodes=0;
      const pending=[{node:template.content,depth:0}];
      while(pending.length) {
        const {node,depth}=pending.pop();
        if(++nodes>65536 || depth>128) return {state:"too_large"};
        if(node.nodeType===Node.ELEMENT_NODE && node.tagName==="TEMPLATE") pending.push({node:node.content,depth:depth+1});
        for(let child=node.lastChild;child;child=child.previousSibling) pending.push({node:child,depth:depth+1});
      }
      const controls=[...template.content.querySelectorAll("[data-sp-canvascontrol]")];
      if(controls.length>4096) return {state:"too_large"};
      result.pageControls=[];
      const loss=code=>{
        const existing=losses.find(item=>item.Code===code);
        if(existing) existing.Count++;
        else losses.push({Code:code,Count:1});
      };
      let textBytes=0;
      const ignored=new Set(["SCRIPT","STYLE","NOSCRIPT","TEMPLATE","SVG"]);
      const blocks=new Set(["P","DIV","LI","UL","OL","TABLE","TR","TD","TH","BLOCKQUOTE","H1","H2","H3","H4","H5","H6","PRE"]);
      for(let ordinal=0;ordinal<controls.length;ordinal++) {
        const control=controls[ordinal];
        const observation={Ordinal:ordinal,ID:null,Type:null,State:"unmapped",Texts:[]};
        result.pageControls.push(observation);
        let data;
        try {data=JSON.parse(control.getAttribute("data-sp-controldata"));} catch {loss("control_data_unmapped");continue;}
        if(!data || typeof data!=="object" || Array.isArray(data) || !Number.isSafeInteger(data.controlType)) {
          loss("control_data_unmapped");continue;
        }
        observation.Type=data.controlType;
        if(uuid(data.id)) observation.ID=normalUUID(data.id);
        else loss("control_id_unmapped");
        if(data.controlType===0) {observation.State="layout";continue;}
        if(data.controlType!==4) {observation.State="omitted";loss("control_text_unmapped");continue;}
        observation.State="text";
        const rich=[...control.querySelectorAll("[data-sp-rte]")].filter(node=>node.closest("[data-sp-canvascontrol]")===control);
        if(rich.length===0) {observation.State="unmapped";loss("rich_text_unavailable");continue;}
        for(const root of rich) {
          const segments=[];
          const walk=[{node:root,exit:false}];
          while(walk.length) {
            const {node,exit}=walk.pop();
            if(node.nodeType===Node.TEXT_NODE) {segments.push(node.nodeValue || "");continue;}
            if(node.nodeType!==Node.ELEMENT_NODE) continue;
            if(node!==root && (node.hasAttribute("data-sp-canvascontrol") || node.hasAttribute("data-sp-rte"))) continue;
            if(node.namespaceURI!=="http://www.w3.org/1999/xhtml" || ignored.has(node.tagName.toUpperCase())) continue;
            if(node.tagName==="BR") {segments.push("\n");continue;}
            if(exit) {if(blocks.has(node.tagName)) segments.push("\n");continue;}
            walk.push({node,exit:true});
            for(let child=node.lastChild;child;child=child.previousSibling) walk.push({node:child,exit:false});
          }
          const text=segments.join("").replace(/\n+$/,"");
          const bytes=new TextEncoder().encode(text).byteLength;
          textBytes+=bytes;
          if(bytes>262144 || textBytes>1048576) return {state:"too_large"};
          observation.Texts.push(text);
        }
      }
      losses.sort((a,b)=>a.Code.localeCompare(b.Code));
      result.state="page_observations";
    }
    if (args.Kind==="stream") {
      if(!file.DriveID || !file.ItemID) return result;
      const collectionRead=await read(args.Site+"/_api/v2.0/drives/"+encodeURIComponent(file.DriveID)+"/items/"+encodeURIComponent(file.ItemID)+"/media/transcripts");
      if(collectionRead.state!=="ok") {
        if(["no_access","not_found","timeout"].includes(collectionRead.state)) return {...result,state:collectionRead.state,status:collectionRead.status || 0};
        return {state:collectionRead.state,status:collectionRead.status};
      }
      const collection=unwrap(collectionRead);
      if(!collection || !Array.isArray(collection.value)) return {state:"malformed"};
      if(collection.value.length>128) return {state:"too_large"};
      if(Object.hasOwn(collection,"@odata.nextLink")) {
        if(!string(collection["@odata.nextLink"])) return {state:"malformed"};
        return {...result,state:"collection_incomplete"};
      }
      const ids=new Set();
      for(const transcript of collection.value) {
        if(!transcript || typeof transcript!=="object" || !string(transcript.id) || !transcript.id ||
            !optional(transcript.temporaryDownloadUrl) || ids.has(transcript.id)) return {state:"malformed"};
        ids.add(transcript.id);
      }
      if(collection.value.length===0) return {...result,state:"no_transcript"};
      let selected;
      if(args.TranscriptID) selected=collection.value.find(transcript=>transcript.id===args.TranscriptID);
      else if(collection.value.length===1) selected=collection.value[0];
      else return {...result,state:"transcript_selection_required"};
      if(!selected) return {...result,state:"no_transcript"};
      if(!selected.temporaryDownloadUrl) return {...result,state:"metadata_only"};
      let download;
      try {download=new URL(selected.temporaryDownloadUrl);} catch {return {state:"malformed"};}
      if(download.protocol!=="https:" || download.username || download.password || download.origin!==location.origin ||
          (download.port && download.port!=="443")) return {...result,state:"unsupported_download_host"};
      download.searchParams.set("format","json");
      const bodyRead=await read(download.href);
      if(bodyRead.state!=="ok") {
        if(["no_access","not_found","timeout"].includes(bodyRead.state)) return {...result,state:bodyRead.state,status:bodyRead.status || 0};
        return {state:bodyRead.state,status:bodyRead.status};
      }
      const body=unwrap(bodyRead);
      if(!body || !Array.isArray(body.entries)) return {state:"malformed"};
      if(body.entries.length>16384) return {state:"too_large"};
      const entries=[];
      let textBytes=0;
      for(let ordinal=0;ordinal<body.entries.length;ordinal++) {
        const entry=body.entries[ordinal];
        if(entry && typeof entry==="object" && ["id","text","speakerDisplayName","startOffset","endOffset"].some(key=>
            typeof entry[key]==="string" && new TextEncoder().encode(entry[key]).byteLength>262144)) return {state:"too_large"};
        if(!entry || typeof entry!=="object" || !string(entry.id) || !entry.id || !string(entry.text) ||
            !optional(entry.speakerDisplayName) || !optional(entry.startOffset) || !optional(entry.endOffset)) {
          return {state:"malformed"};
        }
        textBytes+=new TextEncoder().encode(entry.text).byteLength;
        if(textBytes>1048576) return {state:"too_large"};
        entries.push({Ordinal:ordinal,ID:entry.id,Text:entry.text,SpeakerDisplayName:entry.speakerDisplayName ?? null,
          StartRaw:entry.startOffset ?? null,EndRaw:entry.endOffset ?? null});
      }
      if(controller.signal.aborted) return {state:"timeout"};
      return {...result,state:"transcript_observations",transcript:{ID:selected.id,Entries:entries}};
    }
    return result;
  } catch {
    return {state:controller.signal.aborted?"timeout":"failed"};
  } finally {
    clearTimeout(timer);
  }
}
