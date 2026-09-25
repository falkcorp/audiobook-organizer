### Fixed

- ABS API: a merged book's old library item ids are now handled in one place for every item route, including bookmarks. Bookmarks made before a merge show on the book again through either id, a bookmark saved through an old id is stored with the surviving book, and each bookmark is returned under the id the app asked with.
- ABS API: AudioBooth's "items finished" count no longer counts a merged book once per id. The progress list in `/api/me` (and login, token refresh, authorize) now carries an extra row for an old item id only when this user's app has actually used that id. A leftover progress row stored under the old book is no longer sent alongside the surviving book's row.
