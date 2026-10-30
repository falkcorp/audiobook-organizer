<!-- file: docs/executive-summaries/2026-10-30-dry-run-flag-spelling-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7d1b94e0-2a6c-4f35-8e17-b03c5a9d42f6 -->
<!-- last-edited: 2026-10-30 -->

# Starting a repair with the other spelling of "preview" no longer does nothing

## Executive Summary

Most repair and clean-up operations start as a preview and only write when the
request says so. That switch can be written two ways, and ten operations
understood only one of them. Someone who asked one of those operations to apply
with the other spelling got a preview instead: the run finished, its summary
read like a normal preview, and nothing was written, so it looked like a repair
that had worked. All ten now understand both spellings. Asking for neither still
gives a preview, and a request that states the two spellings with different
answers is refused with a message naming both, rather than guessed at.

## What changed

- **Ten operations accept both spellings.** They cover series clean-up, author
  clean-up and merging, transcription matching and repair, library-state repair,
  path-prefix rewriting, phantom-series repair and the version-group primary
  repair. Two of them already refused the unknown spelling with an error; the
  other eight dropped it without a word.
- **The duration re-extract operation shares the common check.** It used its own
  copy of the same rule; behaviour is the same, and it now has the same test as
  the others.
- **A new check keeps it that way.** Two tests fail if any operation takes only
  one spelling, or if the switch is ever declared in a way where leaving it out
  would mean "apply".

## What did not change

No operation was found that applied when the switch was left out; the fault was
always a requested apply that silently previewed. A few web endpoints outside the
operations list still apply when the switch is left out. They are an open owner
decision and are untouched here.
