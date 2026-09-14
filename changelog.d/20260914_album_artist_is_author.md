### Fixed

- The iTunes importers read Album Artist as the author, not the narrator, matching the file-tag readers (ALBUMARTIST / aART / TPE2 is the author, owner decision 2026-09-14). The author is Album Artist, else Artist; Artist is the narrator only when both are set and differ.
- The file-tag readers and the refetch-missing-authors job no longer take ALBUMARTIST as the author when it equals the file's own narrator tag and ARTIST names someone else (organize wrote the narrator there between da064ef4c and c81b39801 on 2026-09-13).
- A manual narrator edit now writes the narrator tag. It was sent under the album_artist key, which no tag writer maps, so it never reached the file.
