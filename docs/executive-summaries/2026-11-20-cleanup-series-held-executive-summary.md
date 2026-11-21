<!-- file: docs/executive-summaries/2026-11-20-cleanup-series-held-executive-summary.md -->
<!-- version: 0.1.0 -->
<!-- guid: 6b0f6f0e-3f0c-4b57-9d0e-5a1f2c7d8e41 -->
<!-- last-edited: 2026-11-20 -->

# The series cleanup job stops un-filing books from series you protected

**Date:** 20 November 2026.

## Executive Summary

- The nightly series cleanup tidies series that hold a single book. You can
  protect a series by putting a hold on it. The job respected a hold when it
  came to deleting the series itself, but it took the book out of the series
  first. The series survived and its book quietly lost its place in it.
- The job now looks at the hold before it touches anything. A held series is
  left exactly as it was, and the job's summary says how many it skipped and
  why. A practice (dry) run no longer lists them as things it would clean up.
- A related case remains: a held series with several books that shares a name
  with another series can still have its books moved by the merge step. That is
  filed as its own task.
