### Fixed

- Measure retained LevelDB entry overhead in an isolated test process, so fixture construction and prior tests cannot inflate the pre-load heap baseline. Accounting constants and calibration tolerances are unchanged.
