### Fixed

#### A closing side panel could leave the page unclickable until reload

Same defect as the stuck dropdown fixed alongside this, on a different control:
closing the filter side panel could leave it part-way shut, and every click
afterwards did nothing until the page was reloaded.

As before, nothing looked wrong. The panel had already slid away visually, but
the dimming layer behind it stayed active and invisible over the whole page,
absorbing every click.

Side panels now close immediately rather than sliding out. Opening is
unchanged.

This is worth recording precisely, because it rules out the explanation this
bug was originally given. The side panel already used fixed, explicit animation
timings — the thing that was assumed to be missing from the dropdown case and
assumed to be the cause. It stalls anyway: 17 of 20 runs of the affected
end-to-end test failed with the dimming layer still present after 15 seconds.
So the cause is not a missing fallback timer. Removing the closing animation
removes the window the race needs, which is what actually fixes it.

Why the animation fails to report completion is still unexplained, and is
deliberately not guessed at in the code.
