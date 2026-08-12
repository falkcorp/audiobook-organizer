# Task 027: ACOUSTID-STATS-3 — AcoustID stats UI card on Maintenance tab

**Depends on:** task 026 (endpoint must exist)
**Estimated effort:** S–M
**Wave:** 8 (AcoustID)

## Goal

Add an AcoustID fingerprint coverage card to the Maintenance tab, matching the SHA Duplicate
Detection card style: shows coverage %, "Fingerprint Library" trigger button, status chip.

## Context

- Endpoint: `GET /api/v1/maintenance/acoustid-stats` (task 026)
- Operation to trigger: `"acoustid-scan"` or `"backfill-acoustid"` — find the exact op ID
  by searching for `"acoustid"` in the operation registration code
- SHA card: find the SHA Duplicate Detection card in `Maintenance.tsx` or a maintenance component
  as the visual template to match exactly
- TypeScript API client: add `getAcoustIDStats()` in `web/src/services/api.ts`

## Files to modify

- `web/src/services/api.ts` — add `getAcoustIDStats(): Promise<AcoustIDStats>`
- `web/src/pages/Maintenance.tsx` (or maintenance component) — add card

## Instructions

### 1. Add API call

```ts
// In web/src/services/api.ts
export interface AcoustIDStats {
    total_files: number;
    with_fingerprint: number;
    by_library: Array<{library_root: string; total_files: number; with_fingerprint: number}>;
}

export async function getAcoustIDStats(): Promise<AcoustIDStats> {
    const res = await apiClient.get<AcoustIDStats>('/maintenance/acoustid-stats');
    return res.data;
}
```

### 2. Add card component

Find the SHA Duplicate Detection card in the Maintenance page and copy its structure.
Replace the data source with `useEffect(() => { getAcoustIDStats().then(setStats); }, [])`.

```tsx
<Card>
    <CardHeader title="AcoustID Fingerprint Coverage" />
    <CardContent>
        {stats ? (
            <>
                <Typography variant="h4">
                    {Math.round((stats.with_fingerprint / stats.total_files) * 100)}%
                </Typography>
                <Typography variant="body2" color="text.secondary">
                    {stats.with_fingerprint} / {stats.total_files} files fingerprinted
                </Typography>
                <Chip
                    label={stats.with_fingerprint === stats.total_files ? "✓ Complete" : "Partial coverage"}
                    color={stats.with_fingerprint === stats.total_files ? "success" : "warning"}
                    size="small"
                />
            </>
        ) : <CircularProgress size={24} />}
    </CardContent>
    <CardActions>
        <Button
            onClick={() => triggerOp("acoustid-scan", {})}
            variant="outlined"
            size="small"
        >
            Fingerprint Library
        </Button>
    </CardActions>
</Card>
```

### 3. Wire `triggerOp`

Find how other maintenance cards trigger operations (search for `triggerOp` or `enqueueOp`
in the Maintenance page). Use the same pattern.

## Test

```bash
npm test
make ci
```

Manual: open Maintenance tab, verify AcoustID card shows coverage stats and the
"Fingerprint Library" button triggers the operation.

## Commit

```
feat(maintenance): AcoustID fingerprint coverage UI card (ACOUSTID-STATS-3)
```

## PR title

`feat(maintenance): AcoustID stats card — ACOUSTID-STATS-3`

## After merging

Mark `- [ ] **ACOUSTID-STATS-3**` as `- [x]` in `TODO.md`.
