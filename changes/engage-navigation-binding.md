### Fixed

- Tool-owned browser navigation waits for the requested document's committed loader and complete readiness instead of accepting a completed previous document. Same-document navigation retains its bound loader; missing identity, downloads and superseding documents fail closed without changing acquisition deadlines or Engage evidence checks.
