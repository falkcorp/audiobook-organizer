### Fixed

- **Which conformance allowance applied to a field could vary between runs.** `allowedAt`
  ranged over the allowance map and returned the first pattern that matched. Go randomizes
  map iteration, so where two patterns could claim one path, the bound actually applied was
  decided by run order.

  Nothing in the suite as declared was ambiguous, so no bound was being mis-applied — but
  the design's whole guarantee is that a bound states the widest gap its stated cause can
  produce, and that guarantee does not survive "whichever pattern the map yielded first."
  A run that picked a 3.0 s bound where 0.5 s was intended would accept a real divergence
  and report green, which is worse than the divergence because it is invisible.

  An exact key now wins outright, and an ambiguous path is reported as an **authoring
  error** rather than resolved by a tie-break: two patterns claiming one field means the
  author has not said what they meant, and guessing is the thing this design exists to
  avoid. Three tests cover it, one looping 200 times because a single call could pass a
  hundred times and fail the hundred-and-first — exactly the shape of bug that gets closed
  as "could not reproduce".

### Removed

- **`todo.d/20260812-abs-conformance-onefailure-unreproduced.md`**, which recorded a
  `TestSearch_ConformsToOracle` failure that then would not reproduce in 27 runs. It is
  explained, so it should not be filed as open work.

  The failing run predated two allowance entries that were added *in response to it*. At
  that moment `book[].libraryItem.media.duration` was declared `Within: 0.5` — which is why
  the failure text says "EXCEEDS the 0.5 allowed", quoting the value that was really in the
  file — and `book[].libraryItem.media.tracks[].startOffset` was not declared at all, which
  is why its five findings were unallowed. Both were fixed in the next edit: the bound went
  to 3.0 because `book[1]` is the six-track book whose total accumulates six roundings, and
  the `startOffset` key was added. The 27 clean runs afterwards are the fix working, not a
  heisenbug going quiet.

  The note's *hypothesis* was a different matter and was correct — see above. It was a real
  latent defect in the matcher, found by taking an unexplained observation seriously rather
  than by anything the observation actually demonstrated.
