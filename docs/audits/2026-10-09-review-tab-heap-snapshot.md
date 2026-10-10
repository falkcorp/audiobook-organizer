<!-- file: docs/audits/2026-10-09-review-tab-heap-snapshot.md -->
<!-- version: 1.0.0 -->
<!-- guid: 24732b59-4515-42ce-81ee-e36e54b8ee25 -->
<!-- last-edited: 2026-10-09 -->

# Review tab heap measurement (2026-10-09)

## Purpose

The owner reported a Review tab using about 16 GB, while the Metadata lane accounts for at most 0.25 GB by
calculation (appendix D section 1 of the filter-identification proposal). This note holds the exact measurement
procedure and empty tables so the owner, who has the production browser session, can record figures and the next
PRs are decided on measurements rather than a guess.

**Snapshot files are never committed and never attached to an issue.** They contain every library title and path,
and this repository is public. Only figures go in the tables below. Rename this file to the day the measurement
ran if that is not 2026-10-09.

## What to record

Run all three parts in Chrome on the owner's machine against production, on Review > Metadata.

### Part 1: which process is large (Chrome Task Manager)

Open the Task Manager with Shift+Esc. For the Review tab's renderer, the GPU process and the browser process,
record the Memory footprint and JavaScript memory (MB) columns in Table 1.

A large figure on the GPU process or the browser process is not this lane's heap and ends the lane investigation.

### Part 2: three heap snapshots (DevTools > Memory > Heap snapshot)

Take three snapshots on Review > Metadata:

- (a) idle, after first paint;
- (b) after one `refresh()` (apply one book, let the poll settle);
- (c) after ten applies in one batch, settled.

For each, record in Table 2 the total JS heap (MB); the count of `Array` objects with more than 10,000 elements
(these are `results` arrays); the retained size of those arrays (MB); the retained size of the largest `Map` (the
`rowStates` map) (MB); and the detached DOM node count.

### Part 3: the index response (DevTools > Network)

Find the `view=index` request. Record the response size (MB) and the `total_count` from its summary in Table 3, so
the rows-to-bytes ratio can be checked against appendix D's 3.4 KB per row.

## Table 1

Chrome Task Manager (Part 1).

| Process             | Memory footprint (MB) | JavaScript memory (MB) |
| ------------------- | --------------------- | ---------------------- |
| Review tab renderer | TBD (owner)           | TBD (owner)            |
| GPU process         | TBD (owner)           | TBD (owner)            |
| Browser process     | TBD (owner)           | TBD (owner)            |

## Table 2

Heap snapshots (Part 2).

| Snapshot                                    | Total JS heap (MB) | Arrays over 10,000 elements (count) | Retained size of those arrays (MB) | Retained size of largest Map, rowStates (MB) | Detached DOM nodes (count) |
| ------------------------------------------- | ------------------ | ----------------------------------- | ---------------------------------- | -------------------------------------------- | -------------------------- |
| (a) idle after first paint                  | TBD (owner)        | TBD (owner)                         | TBD (owner)                        | TBD (owner)                                  | TBD (owner)                |
| (b) after one refresh, settled              | TBD (owner)        | TBD (owner)                         | TBD (owner)                        | TBD (owner)                                  | TBD (owner)                |
| (c) after ten applies in one batch, settled | TBD (owner)        | TBD (owner)                         | TBD (owner)                        | TBD (owner)                                  | TBD (owner)                |

## Table 3

Network panel (Part 3).

| Request    | Response size (MB) | total_count |
| ---------- | ------------------ | ----------- |
| view=index | TBD (owner)        | TBD (owner) |

## Findings

Empty until the owner records the figures. If a retained-array finding applies (see the decision rule), name the
closure found in the DevTools Retainers view here. Figures and closure names only; no titles, authors or paths.

## Decision rule

- If the Task Manager shows the large figure on the GPU process or the browser process, it is not this lane's heap:
  record that and end the lane investigation.
- If the number of arrays over 10,000 elements is above 1 in snapshot (b) or (c), a previous result array is being
  retained across refreshes. The finding must name the closure that holds it (DevTools Retainers view) and is
  handed to 02-PR18 as an item to fix in the same rewrite of the lane's effect chain.
- If the Review renderer is under 300 MB in all three snapshots (a), (b) and (c) and the 16 GB is elsewhere, say so
  here and close the lane question.
