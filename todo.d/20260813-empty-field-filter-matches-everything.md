## ✅ An empty `FieldFilter` value matched the WHOLE LIBRARY (fixed)

`fieldMatchesValue` ends in:

```go
return strings.Contains(strings.ToLower(bookValue), strings.ToLower(value))
```

`strings.Contains(anything, "")` is **always true** in Go, so a filter whose value is empty
constrains nothing. Measured live on prod 2026-08-13 (post-deploy build), and the filter is
otherwise healthy — this is specific to the empty value:

| `filters=[{"field":"title","value":X}]` | total |
|---|---|
| `X = ""` | **63,870 — the entire library** |
| `X = "zzqqxx"` | 0 |
| `X = "Skills"` | 25 |

### Why this was more than a confusing read

`FieldFilters` also flow into `Server.resolveFilterToBookIDs`
(`internal/server/metadata_ops.go:458`), which resolves a `FilterSpec` into concrete book
IDs **for background operations** with `limit=100000` — used by
`metadata_batch_candidates.go:59` and the bulk metadata fetch op. An empty value there
silently retargets a scoped job at the whole library. That is the **base64 op-params defect
(#2309) one level down**: same shape, same whole-library default, different entry point.

### Fixed in all three layers that were silent

1. **HTTP boundary** (`handlers/audiobooks/handler.go`) — 400 naming the offending field.
2. **Background-op path** (`metadata_ops.go`) — `resolveFilterToBookIDs` returns an error;
   params arrive already deserialized from the queue, so there is no HTTP boundary here.
3. **Matcher** (`service_filtering.go`) — `matchesFieldFilters` fails **closed** on an empty
   value. Matching nothing is visibly wrong and harmless; matching everything is invisibly
   wrong and, on the op path, destructive. `Negated` is deliberately not consulted —
   neither `f == ""` nor `f != ""` is a constraint anyone can have meant.

No in-repo code constructs an empty-value filter (checked: the list warmer's ~20
constructions all carry real values), so layer 3 only fires on input the boundary should
already have rejected.

### Not addressed

- **The frontend was not changed.** Whatever sends an empty value will now get a 400
  instead of the whole library. That is the intended, safe direction, but the UI path that
  produces it has not been traced — worth doing so the user sees a sensible message rather
  than a raw error.
- Whether any *stored* smart playlist or saved filter contains an empty value was not
  checked; such a filter now returns nothing instead of everything.
