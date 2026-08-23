### Fixed

- AI diagnostics analysis now reports its own progress while it runs. The
  request went off into a background task that the rest of the system did not
  know about, so the page could only show what little that task wrote down by
  hand — and if it died early, nothing said so and the run simply sat there.

- Results from a long AI analysis no longer go missing. Collecting them depended
  on finding the run among the hundred most recent operations, but a batch is
  allowed to take up to a day, by which time a busy library had pushed it out of
  that list. When that happened the finished results were thrown away and
  nothing reported a problem.

- Submitting an analysis while AI is switched off used to report success and
  quietly produce nothing. It now says plainly that it could not submit.

- The confirmation message no longer reads "AI analysis submitted: undefined
  request(s)" — it never had a number to show at that point, and the count now
  arrives with the progress updates instead.
