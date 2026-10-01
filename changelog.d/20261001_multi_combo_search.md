- Metadata search: the "stop at the first rung that answers" ladder is replaced by a multi-combination fan-out that runs on every search, interactive and batch. `parseSearchTitle` cleans the book's title and turns it into up to 4 provider questions. It strips:
  - the organizer's " - Unknown Author" suffix;
  - "read by X (Title)" and a trailing "(Narrator)";
  - series decorations: "Series 17: Title", "Series - 4 - Title", "Series, Book 5 - Author";
  - a leading year, "(Unabridged)" and a genre subtitle.

  The questions run in order of expected value:
  1. title + author;
  2. title + narrator (taggers swap the two);
  3. the literal title, whenever the cleaning changed it;
  4. the existing anchored ladder variants;
  5. the title without a leading year or subtitle;
  6. series + author, kept only at the parsed position;
  7. title alone, asked only when nothing else answered, and kept only if it names one of the book's people.

  A bare "Name N: subtitle" is split into series and title only when the subtitle has words of its own and is not a genre tagline. So "Rogue Ascension 8: A Progression LitRPG", "Fahrenheit 451: A Novel" and "Catch 22: A Novel" are searched as written, and a number in a title is a position hint only.
- Per-source call policy:
  - Audible is asked every variant.
  - Open Library is asked at most twice, and the second time only when its first answer was empty.
  - Google Books (a 1,000/day key quota), Hardcover and Wikipedia are asked only the best variant.
  - Audnexus has no title search, so it is no longer title-searched (before, every rung spent a request on its `/authors` endpoint and got nothing back). It is asked only by ASIN: one lookup per book, at most 3 regions.

  Variants run in rounds. The fan-out stops after the first round that pools a strong match: a person of the book's, the title (or slot name), the parsed series position when there is one, and a runtime within 15%. The book's own ASIN also counts as a strong match, but only when the answer also agrees on title words, a person or runtime.
- Search ranking: results are pooled across variants and sources and deduplicated by ASIN/ISBN. A result with no ID is deduplicated by normalized title + author, so two editions of one title both reach the ranking.
  - A result carrying the book's own ASIN is ranked first only when it agrees with the book on title, a person or runtime. A disagreeing one gets only the x2.0 ASIN multiplier.
  - A result whose author is the book's narrator gets a swapped credit (x1.4) instead of the 0.7 author-mismatch penalty.
  - A result naming a different series position than the title's is dropped.
  - A genre-tagline subtitle adds no title words.

  A failed direct ASIN lookup now marks that source as failed.
- The fetch cache now keys every variant separately (`<provider>#q<digest>` rows under the book's existing prefix). It still honors `SearchOptions.BypassFetchCache` and still never caches an empty answer. The pooled per-provider row is still written for the single-book and bulk fetch paths. `searchInputVersion` is bumped to "2", but a row stamped with the version "1" fingerprint for the same inputs keeps its candidates valid for the batch skip and the apply gate. Only its "nothing found" verdict is asked again, so the bump does not re-spend the Google Books quota on every book.
- Candidate fetch op sizing: `metafetch.SourcesBudget` now reports `CallsPerBook`, `BindingID` and `BooksPerSec` from the search's own worst-case per-source call count (`MaxSearchCallsPerBook`: Audible 5, Open Library 2, Audnexus 3, others 1), and the op's log names the binding source. Each round's goroutine limit is sized to the round.
