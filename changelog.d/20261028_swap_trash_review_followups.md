### Fixed

#### Trashing the active version: the trashed-versions banner says what each cleanup group means, and a book's path is listed once

The Version Management page, narrowed to a trash cleanup run, said every group
except the kept one "could not purge and does not retry". Only the needs-attention
group is left alone by the nightly run. The banner now says, per group, what
happened and whether the run tries again: versions the cleanup could not judge
(a drive that is down looks the same), versions whose book serves nothing that
exists on disk (listed every run until the book's path is fixed or the version
restored), and versions whose book still plays one of their files. The run's
result also named a book's path once per trashed version, so two trashed versions
of one book gave the page a repeated list key; each path is now named once.

The 500 that says the switch before trashing the active version could not finish
no longer says a repeated delete is always refused: it is refused while the
version is still mid-switch, but a version that had already reached alt can be
trashed again. A test pins that a failed mark followed by a failed restore of the
leaving version is reported as an unfinished swap, and another that a move of a
file onto itself is refused.
