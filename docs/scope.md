# Scope

m365crawl is in maintenance mode: it gets fixes for breakage and security updates, and no new sources. Something new is on its way. The name is broad, not a claim that every Microsoft 365 app is supported. Your apps, account configuration and cached history may differ.

## Available now

`drafts list/show` inspect only strict own-account regular-compose bodies already
retained as generic records. This is an archive-only cached projection, not a
current-unsent feature or editor renderer. Native evidence is one fresh Mac
Space/team-side body (October 16, 2026) and four removed historical Chat bodies
as shape evidence, not current chat. Named channel UI, native Windows, app-version
range, lifecycle/key continuity and all-drafts completeness are unqualified.
Unknown representations are withheld; source-wide omissions cannot be allocated
to a draft store. A later empty cache does not erase prior evidence or prove send/
discard. These commands do not block independently usable archive workflows.

- **From `sync`:** Teams chats and channels, Outlook mail on macOS and Windows, the calendar, and meeting recaps and action items. Teams messages carry the app, reply and citation claims of archived bot replies (`app_observation`).
- **From their own explicit `sync`:** local OneNote indexed notes, source-qualified Outlook people observations, native meeting attendee evidence from Mac HxStore or supported copied profiles, Office and OneDrive document metadata, and the text of cached Loop channels as partial snapshots ([local sources](local-sources.md)).
- **Opt-in, from its own explicit `scout sync`:** Microsoft Scout session history from the session files Scout keeps in your local OneDrive folder, never downloading a cloud-only file. macOS and Windows read it; Linux reads a named folder ([Scout session history](scout.md)).
- **Only when you ask:** meeting transcript text through `transcripts fetch`, the text of one resident Word, Excel or PowerPoint file through `content capture`, one SharePoint page or Stream transcript per `content fetch page/stream`, and bounded Engage Home starters or a selected thread through `content fetch engage-home` / `content fetch engage-thread URL` ([explicit content capture](content.md)). Excerpts of one OneDrive/SharePoint Office file, including a sensitivity-labeled encrypted one, through `content fetch doc LINK` and the optional WorkIQ source, only when your rights on that file include extracting its text. Selected thread capture includes observed replies/authors/clocks, safe links and generic attachment metadata, not attachment downloads or server completeness.

An archived recap action item is not a Planner or To Do task, a native people observation is not a resolved directory identity, and an archived bot citation is not private Copilot history. Native attendee evidence describes the reader-selected source detail, not a resolved person or current attendance; native Windows/OWA acquisition remains unqualified even though archive reads are portable. The platform differences and cache limits are in the [README](../README.md#limits), the [usage guide](guide.md) and the pages linked above.

## A complement to Graph and MCP tools

The [optional WorkIQ source](commands.md#workiq) is an explicit bridge to the user's already installed WorkIQ subprocess, not m365crawl's own Graph client. Mail can fill a failing desktop source for an already archived account; coverage compares selected chat counts. The conservative production branch withholds unqualified chat/channel/reply message rows and cross-source clock overrides. It does not claim complete Microsoft 365 history, native WorkIQ/Windows qualification or new account registration. The only writes are the person's directed writes through the [`writes` commands](commands.md#writes): chat messages and reactions, mail, replies and reply-alls, and drafts, after the person turns writes on. Ordinary recall remains offline; no source probe runs implicitly.

m365crawl is not a replacement for every job. Use Graph or MCP tools when you need to post in a channel, retrieve data the desktop apps never cached, or work on an unsupported host or app. Keep m365crawl for local recall, archive search and context-building from the sources it actually covers. Missing or stale cache data stays visible rather than becoming a claim that the work does not exist.

## Reporting breakage

m365crawl takes no feature requests. If something that used to work breaks, [open an issue](https://github.com/ourostack/m365crawl/issues) with your operating system, app version and what changed. Do not attach real mail, chats, document contents, cache files, archive databases, credentials or tokens to public issues. Use synthetic examples and non-sensitive counts, formats and field names.
