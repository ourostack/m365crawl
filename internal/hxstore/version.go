package hxstore

// ReaderVersion names the container layout this package was written against: a
// 40-byte block header with two CRC-32 checksums, the block magic, type 8 and an
// LZ4 block payload, measured on Outlook for Mac 16.115. It changes only when
// the block layout this package accepts changes, so a caller can record it next
// to what it read.
const ReaderVersion = 1
