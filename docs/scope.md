# Scope, roadmap and prompt requests

m365crawl is built around the maintainer's own Microsoft 365 usage: support grows from data we can inspect and workflows we can test on real machines. The name is the direction, not a claim that every Microsoft 365 app is supported. Your apps, account configuration and cached history may differ.

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

The [optional WorkIQ source](commands.md#workiq) is an explicit bridge to the user's already installed WorkIQ subprocess, not m365crawl's own Graph client. Mail can fill a failing desktop source for an already archived account; coverage compares selected chat counts. The conservative production branch withholds unqualified chat/channel/reply message rows and cross-source clock overrides. It does not claim complete Microsoft 365 history, native WorkIQ/Windows qualification or new account registration. The only writes are the person's directed writes through the [`writes` commands](commands.md#writes): a Teams chat message, a new mail draft or a reply to one mail, after the person turns writes on. Ordinary recall remains offline; no source probe runs implicitly.

m365crawl is not a replacement for every job. Use Graph or MCP tools when you need to reply-all, send new mail, react, post in a channel, retrieve data the desktop apps never cached, or work on an unsupported host or app. Keep m365crawl for local recall, archive search and context-building from the sources it actually covers. Missing or stale cache data stays visible rather than becoming a claim that the work does not exist.

## Planned expansion, not shipped support

| Area | Direction | What still needs proving |
| --- | --- | --- |
| People resolution and meetings | Qualify subject-scoped identity links and additional native attendee platforms | Positive subject tenancy and platform-specific mappings; existing native attendee evidence preserves own-snapshot ambiguity and source/read/link scope rather than resolving identity or current attendance |
| Office and OneDrive | Resolve file/library associations, and discover document sources natively on Windows | Provider/residency evidence, ambiguous identities and Windows-specific sources; metadata alone is not file or text access |
| More OneNote coverage | Qualify additional native app layouts and page links | Windows app-cache discovery, coherent WAL capture and account-qualified page URLs; an index is not a complete notebook export |
| Loop links | Link cached Loop channels to their pages, messages and authors | Preserve Teams provenance, snapshot versions and partial coverage; cached channel text is not a full page |
| Browser-backed content beyond one target | Read cloud document text and SharePoint News, go beyond one explicit page or Stream transcript, and cover Engage surfaces beyond bounded Home/selected-thread observations | A tested read path and archive integration for each service; one working service does not prove another, and native Windows acquisition is still unqualified |

**Not included in the current expansion:** assigned Planner/To Do tasks and private Microsoft 365 Copilot chat history. Our probes have not established a representative, account-qualified read contract for Planner/To Do tasks. Copilot chat history has no route for one signed-in user: Microsoft's export of Copilot interactions, [`getAllEnterpriseInteractions`](https://learn.microsoft.com/en-us/microsoft-365/copilot/extensibility/api/ai-services/interaction-export/aiinteractionhistory-getallenterpriseinteractions), supports only the application permission `AiEnterpriseInteraction.Read.All`, which an administrator must consent to and which covers the whole organization; delegated (signed-in user) access is not supported. Copilot Cowork runs in the cloud, and the files it creates are saved to OneDrive or SharePoint, where they already appear as ordinary documents. Recap action items are not assigned tasks, and Teams bot citations are not private Copilot history. Microsoft Scout session history is a separate, opt-in source ([Scout session history](scout.md)). Parsing OneDrive's local sync-cache database is backend groundwork only: coherent capture while OneDrive is changing that database is not yet qualified. This limitation concerns OneDrive's cache, not m365crawl's existing `sync` command.

Lists can follow a working SharePoint reader when there is data and a concrete use. Forms, Whiteboard, Viva Insights and separate Bookings support are deferred: they are not part of the current implementation commitment. Security and identity apps are outside the work-content scope. These are priorities, not declarations that those products can never be supported.

The goal is one connected archive, not an unrelated crawler per app. New sources should improve shared search, people resolution or links between work items, preserve the distinction between cached and fetched data, and make missing or stale content visible. Planned capabilities will be documented as available only after they ship and their platform support is verified.

## Request support with a prompt request

**Missing something you use? Work with your agent to open a GitHub issue as a "prompt request".** Describe the job you want the agent to do, the Microsoft 365 app, your operating system and app version, and what happens today. Your agent can help check whether a readable cache or a browser read path exists, but you do not need to reverse-engineer it or submit code before asking.

For example:

> Add support for my Microsoft 365 app so my agent can answer this question: [the job]. I use [app and version] on [operating system]. Today m365crawl [what it does or cannot do]. Please investigate the available data, explain any coverage limits, and propose how it would join the existing archive.

Search [existing issues](https://github.com/ourostack/m365crawl/issues) first, then [open a prompt request](https://github.com/ourostack/m365crawl/issues/new?template=prompt-request.yml). The form starts with the job you want your agent to do; technical evidence is optional. Requests help choose the next integrations; they are not a promise of support or a delivery date. Do not attach real mail, chats, document contents, cache files, archive databases, credentials or tokens to public issues. Use synthetic examples and non-sensitive counts, formats and field names.
