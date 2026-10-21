### Fixed

- Wait for the owned file-event pump and its watcher-close call before returning
  from watch shutdown, rather than letting that cleanup continue after return.
