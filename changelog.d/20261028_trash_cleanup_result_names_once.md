### Fixed

#### Trash cleanup results name each book and kept file once

A book with two trashed versions in one group of a cleanup run was listed twice
in the run's stored result, and so were the kept files two versions named. The
operation page keys those lists by value, so the repeat was a duplicate key. The
result now names each book id and each kept path once, in the order first met;
version ids stay one per version, so the counts are unchanged.
