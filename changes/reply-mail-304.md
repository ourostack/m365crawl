### Added

- `writes reply-mail` sends a plain-text reply to one mail the person directed an agent to answer, through WorkIQ (#304, part of #250). The reply goes to the mail's reply-to addresses, else its sender, and the request must name exactly them (`writes target --message ID` prints them and the mail's version). m365crawl makes the reply draft, checks it is addressed exactly as asked with the request's text, sends it, and counts it as sent once it is found in Sent Items; a draft that does not match stays in Drafts and nothing is sent. The archive (schema 28) records each reply's draft.
