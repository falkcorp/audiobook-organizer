### Fixed

#### Background jobs no longer run with settings they failed to read

Wave 3 of the silent-failure sweep. Thirteen background operations — library scan,
organize, folder auto-scan, both iTunes path jobs, both OpenLibrary jobs,
diagnostics export, and five maintenance jobs — read their settings from the job
record and **threw away any error from reading them**. If the settings could not
be understood, every one silently reverted to its default and the job ran anyway.

The default is not "do nothing". It is usually "do everything".

The sharpest case is **Organize**. It takes a list of the books to organize. If
that list arrived in a shape the server could not read, the error was discarded
and the list came back empty — and an empty list does not mean "organize no
books", it means "no selection given", which downstream means *organize the entire
library*. A request to organize one book could become a full-library run that
moves files on disk. Nothing in the log would say the settings were ever
misread; the job would simply report that it organized far more than you asked
for.

Every one of these jobs now refuses to start and says which setting it could not
read, instead of quietly substituting its own.

Two jobs in the same files already did this correctly and were left alone — they
are what the fix was modelled on.

Two other fixes to the folder auto-scan while in that file: it now reports how
many books **failed** to organize rather than only how many succeeded (a run where
every book failed and a run where every book was already tidy both used to print
the same "0 organized"), and it no longer routes multi-file audiobooks into the
single-file organize path — the same defect fixed in the post-scan organizer,
found in a third copy of the same loop.

Covered by 21 tests that call the real registered jobs. With the fix reverted,
the eight it changed accept unreadable settings and run; the two that were already
correct keep refusing — so the tests distinguish the fix from the pre-existing
behaviour rather than passing on both.
