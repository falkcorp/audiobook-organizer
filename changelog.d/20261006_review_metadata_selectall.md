### Fixed

#### Review → Metadata: select-all is always on screen

"Select all N matching" only appeared after ticking a bare per-page checkbox,
and the checkbox itself vanished at zero rows, so the owner could not find
select-all at all. A selection bar (Select page (N) / Select all N matching /
Clear / X selected) now sits sticky at the top of the main pane in every view
mode, with or without a chip, and in the queue header. Buttons are disabled
rather than hidden when there is nothing to act on. "Select page" toggles, as
the checkbox it replaces did.

#### Review → Metadata: summary chips honour the Title filter regex

A chip view ignored the Title filter entirely. Chip rows are now narrowed by
the regex (other filters stay paused), the banner says which pattern applies,
and changing the regex while a chip is active drops selected books whose title
no longer matches. Selections made in other chips are kept unless their own
title fails. An invalid regex now shows an error under the field instead of
being silently ignored.

#### Review → Metadata: "Min confidence: 190%" relabelled as a match score

Candidate scores are additive evidence sums that routinely exceed 1.0, so the
threshold was neither a confidence nor a percentage. The rail now reads
"Min match score: 190" with a tooltip explaining the scale. The per-row and
spine score chips show the same unitless number. The stored filter value and
presets are unchanged.
