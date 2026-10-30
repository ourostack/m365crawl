# Microsoft Scout session history

[Microsoft Scout](https://learn.microsoft.com/en-us/microsoft-scout/faq) is a desktop agent app for Windows and macOS. Microsoft's documentation says Scout stores your session history per device in your OneDrive. m365crawl can copy those session files into its archive, so an agent can answer "what did my agent do for me today?" next to Teams, mail and calendar.

This source is off until you run `m365crawl scout sync`. Ordinary `sync`, `watch` and shared `search` never read it.

## Privacy

Scout sessions are private AI conversation history: everything the agent saw and did, including tool output that can quote your mail, chats and files. m365crawl handles them like your private chats:

- They are read from your own computer and stay in the local archive. Nothing is uploaded and no network is used.
- Token-shaped values are replaced with `[redacted]` before anything is stored: JWTs anywhere in the text, strings that start with `Bearer`, values of keys such as `access_token` or `password`, and `sig=` URL parameters. Other secrets that appeared in tool output are kept as written, so protect the archive like the conversations themselves.
- Session text is never written to logs or error messages.
- Only `scout list`, `scout search` and `scout show` print it, and only when you ask.

## Read your sessions

```sh
m365crawl scout sync                      # opt in: copy the session files on this computer
m365crawl scout list --since 24h          # sessions with activity in the last day, newest first
m365crawl scout search "quarterly plan"   # matching messages, each with its scout-session:N
m365crawl scout show scout-session:12     # one session with its messages
m365crawl scout sources                   # what each scan read and skipped
```

`scout list`, `scout search`, `scout show` and `scout sources` read only the archive. Run `scout sync` again to pick up new sessions; a file whose size and modification time have not changed is not opened again.

## Where the files are

`scout sync` looks in the `Apps` folder of each local OneDrive folder and keeps any app folder that has a `sessions/devices` folder inside. It finds the folder by that layout, not by its name, because the name can differ between app releases. Each device folder holds one JSON file per session.

| Platform | What `scout sync` reads |
| --- | --- |
| macOS | `~/Library/CloudStorage/OneDrive-*/Apps/*/sessions/devices/*/*.json`, or a folder named with `--root` |
| Linux | Only a folder named with `--root`, on a local disk |
| Windows | Nothing yet: acquisition is refused with `scout_platform_unqualified` until it is verified on a Windows computer. An archive read on Windows works. |

## Files that live only in the cloud

OneDrive can keep a file only in the cloud and download it when it is opened (Files On-Demand). m365crawl never downloads a file. On macOS every read runs with downloads of cloud-only files turned off, so a file that becomes cloud-only during the read fails instead of being fetched.

`scout sync` counts such files as `cloud_only` and skips them, and every Scout result carries `scout_coverage` with those counts. A missing session is therefore not evidence that the agent did nothing.

To include those sessions:

1. In Finder, open your OneDrive's `Apps` folder.
2. Right-click the Scout app folder and choose **Always keep on this device**.
3. Wait for OneDrive to finish downloading.
4. Run `m365crawl scout sync` again.

## What the archive keeps

- Each distinct version of a session file is kept as an immutable observation with its SHA-256 digest. `observation_id` names it; `scout-session:N` names the file and stays the same across versions.
- When a session file disappears from OneDrive, because you or Scout deleted the session, the archive keeps what it read and marks it `file_state` `gone`.
- A file that cannot be read is counted with a fixed reason; m365crawl never guesses its content.

## Format

The session file format is not a documented interface and can change between Scout releases. The reader keeps the fields it knows (session name, times, model, whether an automation started it, and each message's role, time and text), ignores fields it does not know and counts what it could not read in `losses`. `scout_coverage.format` is `unofficial` to say so.

## Copilot and Cowork history

Microsoft 365 Copilot chat history and Copilot Cowork history are not available. Microsoft's only export of Copilot interactions, [`getAllEnterpriseInteractions`](https://learn.microsoft.com/en-us/microsoft-365/copilot/extensibility/api/ai-services/interaction-export/aiinteractionhistory-getallenterpriseinteractions), needs the application permission `AiEnterpriseInteraction.Read.All`, which an administrator must approve for the whole organization; a signed-in user cannot use it for their own history. Files that Cowork creates are saved to OneDrive or SharePoint and appear as ordinary documents.

Every flag and result field is in [commands.md](commands.md#scout-session-history). [SPEC.md](../SPEC.md) is normative.
