### Fixed

- Transcript fetches recognize Microsoft dogfood SharePoint tenant hosts under
  `.sharepoint-df.com`, including personal sites, without weakening host-label,
  site-root, identifier or reference-quality checks. Unsupported hosts now have
  a distinct reason instead of being reported as malformed identifiers.
