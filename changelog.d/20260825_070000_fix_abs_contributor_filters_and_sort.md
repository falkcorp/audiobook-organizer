### Fixed

#### The Audiobookshelf author filter no longer lists authors with no books

Opening the filter menu in an Audiobookshelf client and picking an author often
led to an empty shelf. The filter list was built from every author row in the
database, while the Authors tab beside it was built from the authors who
actually appear on a book the library shows. In the production library those
are very different lists: 4,975 of 12,854 authors — 38.7% — have no visible
book at all. Every one of them was offered as something you could filter by,
and every one of them returned nothing.

The same applied to narrators, from a different source with the same shape.

Both lists now come from the shared contributor index the Authors and Narrators
tabs already use, so what the filter menu offers is exactly what the library can
show you.

#### Sorting the Audiobookshelf authors list now actually sorts it

`?sort=` and `?desc=` were read from the request and echoed back in the response
— the reply said which ordering had been applied — but no sorting ever happened.
The list came back in name order regardless, so sorting by number of books, or
reversing the order, appeared to be accepted and did nothing.

Sorting by name, last-name-first, number of books, date added and date updated
now works, in both directions. A sort this server has no field for is logged
instead of being silently ignored.

### Changed

#### The Audiobookshelf filter menu is no longer rebuilt on every request

The endpoint behind the filter menu is requested during every library page load
and was doing three full passes over the library each time, with no caching. It
took just over 7 seconds per call against the production library, and a second
call cost the same as the first. It is now built at most once every five minutes
and shared, like the author and narrator lists already were.

Separately, when that shared list did need rebuilding, every request that
arrived in the meantime started its own rebuild rather than waiting for the one
already running. With the filter menu now using the same list, that would have
happened on ordinary page loads, so rebuilds are now shared: the first request
does the work and the rest wait for it.
