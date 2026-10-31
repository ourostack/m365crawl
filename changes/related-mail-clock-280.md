### Fixed

- A calendar event's `related_mail` dates mail as `mail list` does: a reply in Sent Items is matched to the 14-days-before to 7-days-after window, and ordered, by when it was sent, not by the received time Outlook stores on it (often when the draft was started). Each `related_mail` item adds `dated_at`, and text output shows it in a `date` column.
