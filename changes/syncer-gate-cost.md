### Fixed

- Reduced repeated archive acquisition in synthetic WorkIQ fetch-failure and recheck tests while retaining all failure samples, real SQLite handles, native-lock checks and existing deadlines. This addresses cumulative verification cost without changing crawler acquisition or reducing release gates.
