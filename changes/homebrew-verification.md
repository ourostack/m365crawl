### Fixed

- Homebrew release verification checks the published cask version without loading
  its Ruby before the fully qualified install. Homebrew's trust defaults and the
  installed-version and signature checks remain unchanged.
