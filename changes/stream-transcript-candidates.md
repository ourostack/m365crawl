### Fixed

- `content fetch stream` on a Stream file with several transcripts and no `--transcript-id` now lists the native `transcript_candidates` (ID plus native display name, language tag and creation-time labels, never download URLs), each with an exact `next` fetch command. Before, the report said a selection was required but gave no way to learn the transcript IDs. Fetch still never picks a transcript, and archived selection-required observations without candidates stay valid.
