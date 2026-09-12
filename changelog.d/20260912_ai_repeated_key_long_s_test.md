### Fixed

- AI reply parsing: added a test that a repeated key spelled with U+017F
  (`ſeries` next to `series`) is rejected. The JSON decoder folds `ſ` to `s`,
  so the two spellings fill the same field. Nothing covered the case, so
  switching the key match from Unicode folding to `strings.ToLower` passed
  every test.
