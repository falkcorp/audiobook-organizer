- Metadata search: the "stop at the first rung that answers" ladder is replaced by a multi-combination fan-out that runs on every search, interactive and batch. `parseSearchTitle` cleans the book's title and turns it into up to 4 provider questions. It strips:
  - the organizer's " - Unknown Author" suffix;
  - "read by X (Title)" and a trailing "(Narrator)";
  - series decorations: "Series 17: Title", "Series - 4 - Title", "Series, Book 5 - Author";
  - a leading year, "(Unabridged)" and a genre subtitle.

  The questions run in order of expected value:
  1. title + author;
  2. title + narrator (taggers swap the two);
  3. the literal title, whenever the cleaning changed it. Its answers must carry the cleaned title's words, so "Frost Heart" cannot answer "Magma Heart - Unknown Author";
  4. the existing anchored ladder variants;
  5. the title without a leading year or subtitle;
  6. series + author, kept only at the parsed position;
  7. title alone, asked only when nothing else answered, and kept only if its author or narrator is one of the book's people, by full name or surname (a shared first name or word is not enough).

  A bare "Name N: subtitle" is split into series and title only when the subtitle has words of its own and is not a genre tagline. So "Rogue Ascension 8: A Progression LitRPG", "Fahrenheit 451: A Novel" and "Catch 22: A Novel" are searched as written. The book name after the number is still kept, whatever the series' word count, to check answers' positions against ("Witcher 4: The Tower of the Swallow").
- Per-source call policy:
  - Audible is asked every variant.
  - Open Library is asked at most twice, and the second time only when its first answer was empty.
  - Google Books (a 1,000/day key quota), Hardcover and Wikipedia are asked only the best variant.
  - Audnexus has no title search, so it is no longer title-searched (before, every rung spent a request on its `/authors` endpoint and got nothing back). It is asked only by ASIN: one lookup per book. The English stores (`""`, `uk`, `au`) are asked first, and `ca`, `in`, `de`, `fr`, `jp` only when all three say not found, so a found ASIN costs at most 3 requests and a missing one 8.

  Variants run in rounds. The fan-out stops after the first round that pools a strong match. Matching title words alone never make a match strong: a sibling, a box set, an omnibus or a sequel ("The Fall of Hyperion") carries them too. A strong match is either:
  - an answer with the book's own ASIN that names no other series position and either runs within 15% of the book or has no title word or number beyond the book's own (a stored ASIN can be a sibling's); or
  - an answer that names one of the book's AUTHORS, by full name or surname (a narrator in common never counts), identifies the book, names no other series position, and whose title has no word or number beyond the book's own title, series and position. A genre word like "Novel" is allowed; a set word like "Books" or "Collection" is not, nor "Abridged". This rule rejects "Jack Reacher, Books 17-19", "A Wanted Man / Never Go Back / Personal", "Foundation and Empire" and "Shogun (Abridged)". When the book has its own runtime, the answer must also run within 15% of it. An explicit series position other than the title's is never strong unless the runtime is within 2%.

  Numbers never drop out of a title comparison. Word matching ignores tokens of two characters or fewer, so every number in an answer's title must be one of the book's own ("Hyperion 2" does not match "Hyperion").
- Series positions: an answer is dropped as another book of the series only on evidence, checked strongest first:
  1. Its title states the series beside another number ("Hemlock Hollow 7: A Cozy Mystery" for book 8). It is dropped whatever else agrees.
  2. Its runtime is within 2% of the book's. It is kept, because a provider may number the book differently.
  3. The provider's explicit series position differs.
  4. Only when there is no explicit position: none of the numbers in its title is the book's position. A title number is weak evidence.

  Cases 3 and 4 are excused when the answer's title carries the book's own name ("The Tower of the Swallow", which Audible numbers #6, for "Witcher 4") and no number the book's own title does not ("Assertions 2" is not excused for "Eternal Dominion, Book 04 - Assertions"). A number in a title never confirms a position. Once two pooled answers carry that name at different positions, the name counts as the series' tagline and excuses nothing. This catches taglines the genre vocabulary does not know ("A Cozy Mystery", "The Saga"). The whole pool is re-filtered after every round.
- Search ranking: results are pooled across variants and sources and deduplicated by ASIN/ISBN. A result with no ID is deduplicated by normalized title + author, so two editions of one title both reach the ranking.
  - A result carrying the book's own ASIN is ranked first only when it names no other series position and agrees with the book on runtime or title. A disagreeing one gets only the x2.0 ASIN multiplier.
  - The direct lookup of the book's stored ASIN is dropped when the store answers with another series position: the stored ASIN was a sibling's.
  - A result whose author is the book's narrator gets a swapped credit (x1.4) instead of the 0.7 author-mismatch penalty.
  - A result naming a different series position than the title's is dropped.
  - A genre-tagline subtitle adds no title words.

  A failed direct ASIN lookup (transport, 5xx, 429) now marks that source as failed. An ASIN no store has (Audible's empty product, or a 404/410 whose body is the provider's own JSON) is an answer, not a failure (`metadata.IsNotFound`), so the book is not re-asked forever. A 404 with an HTML or empty body (a proxy or edge answering) stays a failure. Audnexus reports a region that failed rather than the regions that only said "not here". The old region list asked the US store twice, as `""` and `us`.
- The fetch cache now keys every variant separately (`<provider>#q<digest>` rows under the book's existing prefix). It still honors `SearchOptions.BypassFetchCache` and still never caches an empty answer. The pooled per-provider row is still written for the single-book and bulk fetch paths. `searchInputVersion` is bumped to "2", but a row stamped with the version "1" fingerprint for the same inputs keeps its candidates valid for the batch skip and the apply gate. Only its "nothing found" verdict is asked again, so the bump does not re-spend the Google Books quota on every book. When an empty refetch carries forward version "1" candidates, they keep the version "1" fingerprint. They are not relabelled as this version's answers. Version "1" candidates are filtered by this version's position rules wherever they are read: in the carry-over, in `GetCachedCandidates` (the apply paths) and in the batch verdict. A sibling the old ladder pooled ("Rogue Ascension 7" for book 8) can no longer be applied. The stored row is not rewritten, and only a book whose every legacy candidate was filtered out is asked again, so there is no mass refetch.
- Candidate fetch op sizing: `metafetch.SourcesBudget` now reports `CallsPerBook`, `BindingID` and `BooksPerSec` from the search's own worst-case per-source call count (`MaxSearchCallsPerBook`: Audible 5, Open Library 2, Audnexus 8, others 1), and the op's log names the binding source. Each round's goroutine limit is sized to the round.
- The search's two errgroup `Wait` results (the fan-out round and the scoring group) are logged instead of dropped; `.errcheck-baseline` goes from 830 to 829.
