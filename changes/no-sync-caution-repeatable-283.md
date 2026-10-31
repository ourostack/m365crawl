### Fixed

- A `--no-sync` read of notes, documents, Loop or native people sources prints the same caution every time for the same archive: it now names when the oldest source was last read (an absolute UTC time) instead of how long ago, which changed from one minute to the next (#283).
