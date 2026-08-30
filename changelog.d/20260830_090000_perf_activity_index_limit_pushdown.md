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
order, so iterating the index in reverse yields newest-first directly. The query
now walks the index backwards, decodes only the rows in the requested page, and
never sorts.

Measured on a 50,000-entry operation, three runs each (ranges, not medians —
this path's block cache warms within a run):

| page size | before | after | allocations |
| --- | --- | --- | --- |
| limit 1000 | 556–562 ms | 31–32 ms | 1,106,223 → 18,551 |
| limit 50 | 559–573 ms | 23 ms | 1,106,014 → 1,362 |

That is 17.2–17.9× at limit 1000 and 24.3–25.2× at limit 50 on this machine. An
independent run of the same benchmarks measured 13.4–14.6× and 17.5–19.0×, so
the ratio is sensitive to machine load and **the lower bound is the number worth
quoting**: better than 13× at limit 1000 and better than 17× at limit 50. The
absolute `after` times were stable across every run; it is the `before` baseline
that moves.

#### Fixed: what `total` actually means

An earlier draft of this change claimed the returned `total` was **exactly**
what the old implementation returned. It is not, and the difference is the kind
that raises no error anywhere — `total` feeds the UI's pager and is serialized
with no error channel. The claim has been replaced by a contract that holds, and
by tests that pin each way it can be violated.

`total` is the number of index refs that name **exactly** the requested id and
whose primary row exists, corrected downward for any row inside the page window
that turns out not to decode or not to match. That equals the old total for any
index satisfying this store's own write invariant. Two cases fall outside it,
both only for a row the caller never pages to, and both now pinned to their
exact magnitude by a test rather than described in prose:

- a row that exists but whose stored JSON will not decode;
- a ref whose key names the right id while the row it points at belongs to a
  different one — index corruption the write path cannot produce.

Deciding either costs a `Get` and a decode **per ref**, which is the entire cost
this change removes. Rows inside the page *are* checked, so a foreign row can
inflate a count but can never leak into someone else's transcript. Making it
exact for real means putting the id in the ref value — a schema change with a
backfill, not something to smuggle into a performance PR.

Separately, a ref that merely **prefix**-matched the requested id (`act:op:A:`
also prefix-matches a key for the id `A:B`) used to be counted, and to consume a
rank in the offset skip, which shifted the returned page. Those are now rejected
at scan time by an O(1) key-shape check, so they enter neither the total nor the
rank.

#### Fixed: tied timestamps had no defined order

The retained reference implementation sorted with `sort.Slice`, which is
**unstable** — so for rows sharing a timestamp its order was not merely different
from the new path's, it was not well-defined at all, and "the reverse scan
reproduces the order the sort reached" was a claim about nothing. A fixture with
tied timestamps produced 149 page mismatches between the two.

Both paths now implement one documented ordering contract: newest timestamp
first, and among rows sharing a timestamp, highest ULID first — which for
`ulid.Make`'s monotonic entropy means most-recently-written first. The 149
mismatches are now 0. Timestamps still collide routinely in practice, because
`RecordBatch` writes a batch from instants callers supply at whatever resolution
they have.

#### Fixed: pre-epoch timestamps are refused at write time

`%020d` of a negative Unix-nanosecond value emits a leading `-` (0x2D, below
`0`) instead of a zero pad, so such a key sorts before every other key **and**
sorts in reverse chronological order among other negative keys. Three increasing
instants (1906, 1966, 2023) came back in the order 1966, 1906, 2023 — a silent
wrong order with no error anywhere. `Record` now rejects a timestamp it cannot
key, which is what makes "lexicographic key order is chronological order" true of
every row on disk rather than merely usually true. The rejection is per-entry, so
one bad row does not doom a batch.

Two things protect the rest of the count:

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

**Whoever does fix it must also change the eligibility gate.** The gate accepts
a filter carrying `since`/`until` *only* because neither path honours them.
Teaching the reference implementation to apply a time bound without also making
the gate refuse those filters would make the two paths disagree on every
time-bounded request, silently. A field-count pin on `ActivityFilter` now fails
the suite whenever a field is added, because the gate is a deny-list and an
unclassified field is accepted by omission.
