- [ ] 🏷️ **"Browse by Tag" surfaces internal bookkeeping tags and formats them
      badly.** Owner report 2026-08-10 with a screenshot of the Library page on
      mobile (`books.jdfalk.com`, "Browse by Tag (149)"). The widget is *almost*
      right; every problem below is presentation, not tagging.

      Observed, top five chips in order:

      | Chip as rendered | Count | Verdict |
      |---|---:|---|
      | `dedup:duration-match` | 24,883 | **Strip** — internal dedup bookkeeping |
      | `metadata:language:en` | 15,036 | Keep, but **reformat** (see below) |
      | `metadata:source:audible` | 14,895 | **Strip** — provenance, not a browse axis |
      | `dedup:duration-abridged` | 3,573 | **Strip**, and the count is **suspect** |
      | `science fiction & fantasy` | 1,109 | ✅ this is what the widget is for |

      **What the owner asked for:**

      1. **Strip `dedup:duration-match` entirely.** Nobody browses their library
         by "the deduper thought these two durations matched."
      2. **Strip `metadata:source:audible`** and its siblings (`google-books`,
         etc.). *"For those weird ones like audible metadata source or google
         books or whatever don't put those at the top ever if we can. If we just
         have to hide them that's fine too."* — so a hide/allow-list is an
         acceptable implementation; they do not have to be deleted from the data.
      3. **Strip the `metadata:` prefix and put a space between key and value.**
         `metadata:language:en` should read `language: en`, not
         `metadata:language:en`. Owner: *"for gosh sakes."*
      4. **`dedup:duration-abridged` (3,573) — verify the number before trusting
         it.** Owner: *"not sure on the abridged. That's a weird one. I don't
         think it's as high as you think it is."* Treat this as a **separate
         data question** from the display cleanup: 3,573 abridged editions out of
         the library is a claim the tagger is making, and the owner's intuition
         is that it is over-firing. Do not "fix" it by hiding the tag — measure
         whether the abridged detection is correct first, then decide. Hiding a
         wrong number makes it unfalsifiable.
      5. **Confirm tags are per-BOOK, not per-file.** Owner: *"Also tags are per
         book right?"* This needs a definitive answer from the schema, not an
         assumption — if any tag is stored per `book_file`, a multi-file
         audiobook would inflate every count in this widget by its file count,
         which would independently explain why several of these numbers look too
         high. **Check this before touching the display**, because it changes
         whether the counts above are even meaningful.

      **Suggested shape** (not locked): an ordering/visibility policy for tag
      namespaces rather than per-tag special cases — genuine subject tags
      (`science fiction & fantasy`) rank first, `metadata:*` renders
      prefix-stripped and `key: value` formatted below them, and `dedup:*` plus
      other machine-internal namespaces are hidden from Browse by Tag entirely
      while remaining searchable/filterable for anyone who wants them.

      Screenshot in the 2026-08-10 session; widget renders on the Library page
      under the search box, above `Select All`.
