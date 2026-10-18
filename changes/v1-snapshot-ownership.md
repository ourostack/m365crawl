### Fixed

- Teams and Outlook snapshot roots record protected ownership before source copying. Stale sweeping requires both age and positive owner-death evidence and rechecks root identity; active, reused-PID, uncertain, unprotected and legacy roots are retained so another archive's lazy reader keeps its backing files. Registration failure cleans the new root and fails capture. Windows process-open failures never imply death, so unprovable abandoned roots remain.
