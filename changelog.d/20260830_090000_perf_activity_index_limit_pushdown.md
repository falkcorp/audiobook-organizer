### Changed

#### Operation and book activity queries no longer read the whole operation to return one page

`GET /api/v1/operations/:id/activity` and `GET /api/v1/activity?operation_id=…`
are served from the `act:op:`/`act:bk:` secondary indexes by
`PebbleActivityStore.queryByIndexPrefix`. That function used to collect every
index ref for the id, issue one point-`Get` and one `json.Unmarshal` per ref,
sort the whole decoded result by timestamp, and only then slice out the
requested page. An operation with 50,000 activity rows therefore did 50,000
`Get`s and 50,000 decodes to hand back 1,000 rows — and the default page size of
the operation-transcript endpoint is exactly 1,000.

The index key is `act:op:<op_id>:<20-digit-zero-padded-unix-nano>:<ulid>`. The
fixed-width nanosecond field means lexicographic key order *is* chronological
order, so iterating the index in reverse yields newest-first directly — the same
order the old code produced with a sort over every decoded entry. The query now
walks the index backwards, decodes only the rows in the requested page, and
never sorts.

Measured on a 50,000-entry operation, three runs each (ranges, not medians —
this path's block cache warms within a run):

| page size | before | after | allocations |
| --- | --- | --- | --- |
| limit 1000 | 509–558 ms | 33–37 ms | 1,052,714 → 20,731 |
| limit 50 | 522–663 ms | 24–34 ms | 1,052,425 → 1,735–3,494 |

**The returned `total` is unchanged.** It feeds the UI's pager and is returned
with no error attached, so an inaccurate total would be a silent wrong answer
rather than a visible one. Two things protect it:

- Every index ref's primary row is still checked for existence, because an
  `act:op:` ref outlives the row it points at — deletion paths only recently
  began removing index keys, and on production `act:op:` had grown to roughly
  0.783 GiB of a 1.342 GiB activity keyspace, largely refs whose row was pruned
  months earlier. Counting index keys instead would have inflated the total by
  whatever fraction of the index is stale. The existence check is a key-only
  merge over the primary key space (~330 ns per ref) rather than a point `Get`
  (~7.9 µs per ref) — the `Get`, not the decode, was what the old path was
  actually paying for.
- A filter that reads an entry field the index does not carry — `type`, `level`,
  `source`, `search`, `tags`, `exclude_sources`, `exclude_tags`, `tier`,
  `exclude_tiers`, or the *other* id — falls back to the original
  implementation, which is retained in full and is also the reference the new
  path is differentially tested against.

A stale index ref is skipped without consuming a page slot, so a pruned row
shifts the page rather than silently returning fewer rows than requested.

#### Activity index queries are now visible to the store's decode counters

`queryByIndexPrefix` was the one scan path in `PebbleActivityStore` that decoded
stored entries without incrementing `EntriesDecoded`, and it dropped a row whose
stored JSON would not decode with a bare `continue` — no counter, no log. Both
paths now count their decodes and their drops and report the drops in aggregate,
which is also what makes "this query decodes only the page" an assertable fact
rather than a claim.

#### Known, unchanged: the index path ignores `since` and `until`

`matchesFilter` does not read `f.Since`/`f.Until` and neither implementation
applies a time bound, so `GET /api/v1/activity?operation_id=X&since=…` silently
ignores `since` — as it did before this change. Both paths ignore it
identically. Fixing it is a behavioural change to that endpoint and belongs in
its own PR with its own test, not folded into a performance change.
