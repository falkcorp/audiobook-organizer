### Fixed

- Author splitting no longer manufactures authors from book titles: comma and
  "and"/"&" clauses must be person-shaped (2-4 words, no leading/interior
  function words — name particles like "de"/"van" stay allowed, no subtitle
  punctuation, no trailing parenthetical), and the space-concatenation
  fallback refuses comma-bearing names whose clauses already failed that
  gate. Refused splits stay visibly broken instead of laundering a title
  fragment into a plausible name.
