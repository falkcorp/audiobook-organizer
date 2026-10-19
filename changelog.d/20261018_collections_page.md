A Collections page lets you create and manage static and dynamic collections
from the web app, and the collections you make there appear in Audiobookshelf.
A static collection is a hand-picked list of books that you add to and remove
from one action at a time. A dynamic collection is a saved search written in the
same search syntax as the Library search bar, with an optional sort and limit.
Preview re-runs the search and shows the live result, and Materialize stores it
so the Audiobookshelf app serves it. The query box lists the fields you can use
with an example each, completes field names as you type, shows a parse error
before you save, and offers two ready-made examples. A query that uses a
reading-state field (read_status, progress_pct, last_played) is evaluated as the
person who created the collection, for everyone, and the page says so. People
without the collections.manage permission can see collections and their books
but get no controls to change them.
