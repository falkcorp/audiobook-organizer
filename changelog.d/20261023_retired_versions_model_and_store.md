Added the storage model for retired book versions, the first of five stacked
changes that let the retired-versions fixer retire a trashed duplicate's
files into the live book instead of leaving them stranded. A `book_file` row
now has a `Retired` flag and a book version has a `retired` status, with the
shell it came from and the operation that retired it recorded on the version.
Retiring re-keys the row under the live book and never deletes it; every
default `book_file` read, count and aggregate skips retired rows, and the
generic move and delete primitives refuse them. `RetireBookFiles`,
`UnretireBookFiles` and `RehomeRetiredVersion` are the only operations that
create, undo or move one, exposed as capabilities rather than on the `Store`
interface. Nothing calls them yet, so running behavior does not change.
