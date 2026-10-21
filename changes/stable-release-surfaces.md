### Fixed

- Require Go 1.27.3 or newer so builds include its `crypto/x509` security fixes.
- Preserve supplied release notes and signing disclosures in published releases,
  and refuse promotion when the published body does not match. The stable release
  badge and installation/agent guidance now explicitly identify the stable 1.x
  contract without broadening native source coverage.
