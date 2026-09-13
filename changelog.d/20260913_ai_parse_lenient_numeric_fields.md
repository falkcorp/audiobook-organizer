### Fixed

- AI filename parsing (`library.ai-parse` and the scan's AI batch phase) no
  longer fails a whole batch of up to 8 books when the model returns `year` or
  `series_number` as a string. A string holding a clean number (`"2015"`,
  `" 3 "`, `"3.0"`) is accepted; anything else (`"not available"`, `""`,
  `"3.5"`) leaves that one field empty, and the rest of the book and the batch
  still decode and save. Each coerced field is logged with the filename, the
  field and its raw value. The existing fail-closed rules (result-count
  mismatch, error payloads, wrapper shapes, non-object results) are unchanged.
