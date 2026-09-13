### Fixed

- **iTunes smart-playlist criteria parser (ITUNES-SMARTCRIT-PARSE, #2658).**
  `ParseSmartCriteria` read the blob as a little-endian, fixed 136-byte-stride
  rule array and reported success on every real blob while returning rules with
  no field, operator or operand. It now requires the `SLst` magic (anything else
  is an error), reads big-endian, and recovers UTF-16BE string operands only
  when their length prefix matches, reading field and operator at the measured
  offsets. Only the validated codes are named (fields Album=3, Artist=4, Genre=8;
  operators `0x01000002` contains, `0x03000002` does not contain). Everything
  else is reported in a new `Unresolved` list. `TranslateSmartCriteria` now
  returns an empty query, instead of a partial query or the match-all `*`, for
  anything it cannot express exactly. That makes the importer's
  "refuse to apply empty queries" guard fire as intended. The AND/OR flag, the
  `SLst` container nesting, numeric/date rules and operator `0x01000001` are
  still unmapped.
