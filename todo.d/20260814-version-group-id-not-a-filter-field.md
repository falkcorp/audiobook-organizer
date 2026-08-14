## `?version_group_id=` lists the whole library, and cannot be guarded

Found 2026-08-14 while trying to verify that an author merge had relinked two
books. Every form of the query returned the same answer:

    version_group_id='vg-08c1a396b'          -> count=63869  first=01KNDB8NWHXV2DKRQESBA9SDRA
    version_group_id='vg-TOTALLY-BOGUS-XYZ'  -> count=63869  first=01KNDB8NWHXV2DKRQESBA9SDRA
    version_group_id=''                      -> count=63869  first=01KNDB8NWHXV2DKRQESBA9SDRA

The negative control is the point: a bogus group ID and a real one are
indistinguishable, so the parameter is not read at all. The instrument was
unusable for the verification it was reached for, which is how it was noticed.

**Why the bare-param guard does not cover this.** That guard rejects names in
`audiobooks.KnownFilterFields()`. `version_group_id` is not in it — it is not a
filter field, so `bookFieldValue` has no case for it and the guard has nothing
to match. This is a genuine gap, not the same bug: `?year=` was a *known* field
passed the wrong way; `?version_group_id=` is a field the list never supported.

- [ ] Decide whether the list should filter on `version_group_id` at all. There
      is a real case: the memdb store already indexes it (`memIdxVersionGroupID`
      in `memdb_schema.go`) and `GetAllBooksFrom` accepts it as a filter key, so
      the storage layer supports the lookup the API does not expose.
- [ ] If yes: add a `case "version_group_id"` to `bookFieldValue` and the name
      to `allFilterFieldNames`. `TestFilterFieldNames_MatchTheMatcher` will hold
      the two together, and the bare-param guard then covers it automatically —
      no third list to update. Check the Pebble path too; a memdb-only index
      would be exactly the dual-implementation divergence fixed in #2406/#2410/#2411.
- [ ] If no: it still must not answer with the whole library. Extend the guard
      with a small set of *storage* filter keys that are not list filter fields,
      so the request is rejected rather than silently widened.

⚠️ Whichever way this goes, the rule from `FirstUnknownFilterField` applies: the
two failure modes here are inverted and both misleading — an unknown field
*inside* `filters` matches nothing and answers `count:0`, while a filter field
passed *bare* matches everything and answers with the library. Neither should be
reachable by a typo.
