### Fixed

- Artist tags no longer mint garbage author rows: HTML-entity semicolons are
  protected from the author-separator split (an "&#169;2013 by ..." tag had
  sheared into an "&#169" author), and creation now rejects obviously-dirty
  names (leading "©"/"&#", leading 4-digit year, existing publisher rules) so
  copyright lines become authorless books instead of repair jobs.
