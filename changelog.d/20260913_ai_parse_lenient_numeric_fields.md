### Fixed

- AI filename parsing (`library.ai-parse` and the scan's AI batch phase) no
  longer fails a whole batch of up to 8 books when the model returns `year` or
  `series_number` as a string. A value that is a clean positive integer is
  accepted: a JSON number such as `2015` or `3.0`, or a string such as
  `"2015"`, `" 3 "`, `"+3"`, `"03"` or `"3.0"`. Anything else leaves that one
  field empty -- `"not available"`, `""`, `"3.5"`, exponent forms (`"1e3"` and
  a bare `1e3`), hex, `NaN`/`Inf`, `0` and negatives -- and the rest of the
  book and the batch still decode and save. A result whose only metadata
  fields held such unusable values still fills its slot as an empty result,
  but is not taken as evidence that an unfamiliar wrapper key holds results.
  Each coerced or dropped field is logged with the filename, the field and its
  raw value. The existing fail-closed rules (result-count mismatch, error
  payloads, wrapper shapes, non-object results) are unchanged.
