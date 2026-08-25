### Fixed

#### The authors list no longer claims an ordering it did not apply

Sorting the Audiobookshelf authors list started working in the previous change,
but only for the orderings this server can actually perform. For anything else —
sort by file birthtime, for instance, which nothing in the library records — the
reply still stated that the requested ordering had been applied. It had not; the
list came back in its usual order.

The reply now describes the order the list is actually in. Ask for something we
can do and it says so; ask for something we cannot and it reports the default
order, which is what you received.

The request still succeeds rather than failing. Refusing to return the list at
all because the app asked for an ordering this server does not offer would empty
the Authors tab, which is a worse outcome than showing the authors in a
different order and saying which.
