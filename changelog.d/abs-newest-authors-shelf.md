### Fixed

#### ABS home screen: Newest Authors shows authors new to the library, not the first ten names

The Audiobookshelf-compatible home screen's Newest Authors shelf was the first
ten authors in alphabetical order, each marked as added at the moment of the
request. Names that start with punctuation or a digit sort before letters, so
the shelf was led by leftover tag text stored as authors (an HTML entity, a
copyright line, a file-size fragment, "13 short stories"), all shown as added
today.

The shelf now lists the ten authors who most recently arrived in the visible
library (primary version, organized, not quarantined), newest first. An
author's date is the date added of their earliest visible book, so an author
whose first book was just imported leads the shelf, while an established author
who gains a new book keeps their earlier date. Authors with no visible book, or
whose visible books have no date, never appear. The `addedAt` the app shows for
an author, on the shelf and in the Authors tab, is now that same date instead of
the request time, so sorting the Authors tab by date added works too. The shelf
is chosen when the shared author list is built, so the home page does no extra
library scan.

The author-name creation check now also refuses release-post text such as a
file size ("831MB", "1.2 GB") or a hashtag after a space ("#Winner"), which it
used to accept, including when the space is a non-breaking or full-width space.
A real name with such a group appended ("Name [32k 08;53;59 122MB]") is now
treated as a junk row too, and the junk-author repair reads the name out of the
group and, when that author already exists, proposes moving the books to them
instead of removing the credit.

Leftover junk author rows that already exist are not hidden by this change if
their books are recent; cleaning them up goes through the existing review-gated
junk-author repair.
