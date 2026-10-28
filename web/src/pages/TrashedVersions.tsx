// file: web/src/pages/TrashedVersions.tsx
// version: 1.6.2
// guid: 6f4a5b3c-7d8e-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-28

import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link as RouterLink, useSearchParams } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  Chip,
  Link,
  Paper,
  Stack,
  Tab,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tabs,
  Typography,
} from '@mui/material';
import { apiFetch, isAuthRedirectError } from '../utils/apiFetch';
import { getOperationResult } from '../services/api';
import {
  cleanupSetTab,
  cleanupVersionIds,
  parseCleanupSet,
  parseTrashCleanupResult,
} from '../utils/trashCleanupResult';
import type { TrashCleanupSet } from '../utils/trashCleanupResult';
import { describeRequestError, SESSION_EXPIRED_MESSAGE, unwrapData } from '../utils/apiResponse';
import {
  type BookVersion,
  type PurgeFileNote,
  restoreVersion,
  purgeVersion,
  hardDeleteVersion,
} from '../services/versionApi';
import {
  useConfigurableTable,
  ResizableHeaderCell,
  ColumnPicker,
  type ColumnDef,
} from '../components/common/ConfigurableTable';

const API_BASE = '/api/v1';

// A purge that has not finished (purging) or gave up (purge_failed) has left
// the trash: it cannot be restored, only retried or (purge_failed) hard-deleted.
function purgeUnfinished(status: string): boolean {
  return status === 'purging' || status === 'purge_failed';
}

// versionDate is the date a row is listed under: when it was purged, else when
// it was trashed. Undefined for a trash row with no trashed_at, and for a
// purging or purge_failed row (a purge that did not finish): it has left the
// trash, so its created date must not stand in for a trash date.
function versionDate(v: BookVersion): string | undefined {
  if (v.purged_date) return v.purged_date;
  if (v.trashed_at) return v.trashed_at;
  return v.status === 'trash' || purgeUnfinished(v.status) ? undefined : v.created_at;
}

// filesNote is what the Files column says about a row's files: the ones a
// purge could not remove (purge_failed), else the ones a finished purge kept.
// The full list, with the reasons, is the cell's tooltip.
function filesNote(v: BookVersion): { text: string; detail: string; failed: boolean } | undefined {
  const failed = v.purge_failed ?? [];
  const kept = v.purge_kept ?? [];
  const lines = (notes: PurgeFileNote[]) => notes.map((n) => `${n.path}: ${n.reason}`).join('\n');
  if (failed.length > 0) {
    return {
      text: `${failed.length} file${failed.length === 1 ? '' : 's'} could not be removed`,
      detail: lines(failed) + (kept.length > 0 ? `\n\nKept:\n${lines(kept)}` : ''),
      failed: true,
    };
  }
  if (kept.length > 0) {
    return {
      text: `${kept.length} file${kept.length === 1 ? '' : 's'} kept`,
      detail: lines(kept),
      failed: false,
    };
  }
  return undefined;
}

const COLUMNS: ColumnDef<BookVersion>[] = [
  {
    key: 'book_id',
    label: 'Book ID',
    defaultWidth: 140,
    sortable: true,
    render: (v) => (
      <Link
        component={RouterLink}
        to={`/library/${encodeURIComponent(v.book_id)}`}
        variant="caption"
        sx={{
          fontFamily: 'monospace',
        }}
      >
        {v.book_id.slice(0, 12)}…
      </Link>
    ),
    sortValue: (v) => v.book_id,
  },
  {
    key: 'format',
    label: 'Format',
    defaultWidth: 90,
    sortable: true,
    render: (v) => v.format?.toUpperCase() ?? '—',
    sortValue: (v) => v.format ?? '',
  },
  {
    key: 'source',
    label: 'Source',
    defaultWidth: 120,
    sortable: true,
    render: (v) => v.source,
    sortValue: (v) => v.source,
  },
  {
    key: 'status',
    label: 'Status',
    defaultWidth: 120,
    sortable: true,
    render: (v) => (
      <Chip
        label={v.status}
        size="small"
        color={v.status === 'trash' || v.status === 'purging' ? 'warning' : 'error'}
      />
    ),
    sortValue: (v) => v.status,
  },
  {
    key: 'files',
    label: 'Files',
    defaultWidth: 200,
    sortable: false,
    render: (v) => {
      const note = filesNote(v);
      if (!note) return null;
      return (
        <Typography
          variant="caption"
          title={note.detail}
          sx={{ color: note.failed ? 'error.main' : 'text.secondary' }}
        >
          {note.text}
        </Typography>
      );
    },
  },
  {
    key: 'date',
    label: 'Date',
    defaultWidth: 110,
    sortable: true,
    // Trash rows show when they were trashed -- the date the 14-day purge
    // window counts from -- not when the version was created. A row trashed
    // before trashed_at was tracked has none until the first cleanup run
    // stamps it, so it shows a dash rather than a misleading created date.
    render: (v) => {
      const at = versionDate(v);
      return at ? new Date(at).toLocaleDateString() : '\u2014';
    },
    sortValue: (v) => {
      const at = versionDate(v);
      return at ? new Date(at).getTime() : 0;
    },
  },
];

// What each group of a trash cleanup run means, as the banner finishes the
// sentence "Showing the N versions ...". Each says whether the nightly run
// tries the version again, because the groups differ: only needs_attention is
// left alone (internal/versions/lifecycle.go CleanupResult).
const CLEANUP_SET_TEXT: Record<TrashCleanupSet, string> = {
  kept: 'that cleanup run purged with files kept on disk.',
  needs_attention:
    'that cleanup run could not purge because a file cannot be removed. It does not retry them: Purge now retries one, or hard delete drops it.',
  cannot_tell:
    'that cleanup run left alone because it could not tell whether their files are gone (a drive that is down looks the same). Nothing was deleted, and the next run tries them again.',
  nothing_served:
    "that cleanup run left in the trash because their book serves nothing that exists on disk. Nothing was deleted, and every run lists them again until the book's path is fixed or the version is restored.",
  served_kept:
    'that cleanup run kept in the trash because their book still plays one of their files. Nothing was deleted, and every run tries them again until the version is restored or the book plays something else.',
};

export default function TrashedVersions() {
  // A trash cleanup run's counts link here as ?op=<operation id>&set=kept (or
  // needs_attention): the page then lists only that group's versions, read from
  // the run's stored result, and opens the tab they are on.
  const [searchParams, setSearchParams] = useSearchParams();
  const cleanupOp = searchParams.get('op');
  const cleanupSet = parseCleanupSet(searchParams.get('set'));
  const [tab, setTab] = useState<'trash' | 'purged'>(() =>
    cleanupSet ? cleanupSetTab(cleanupSet) : 'trash'
  );
  // What was read for one (op, set): kept with its key so a stale read for a
  // link the user has since left is never shown.
  const [filterRead, setFilterRead] = useState<{
    key: string;
    ids: string[] | null;
    error: string;
  } | null>(null);
  const filterKey = cleanupOp && cleanupSet ? `${cleanupOp}/${cleanupSet}` : '';
  const filterCurrent = filterRead !== null && filterRead.key === filterKey ? filterRead : null;
  // The version ids of the run's group: null until read, and while no filter is on.
  const filterIds = filterCurrent?.ids ?? null;
  const filterError = filterCurrent?.error ?? '';
  const [versions, setVersions] = useState<BookVersion[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [actionError, setActionError] = useState('');

  const {
    visibleColumns,
    allColumns,
    sortField,
    sortDir,
    columnWidths,
    handleSort,
    toggleColumn,
    isColumnVisible,
    startResize,
    sortRows,
    resetColumns,
  } = useConfigurableTable<BookVersion>({
    storageKey: 'trashed-versions',
    columns: COLUMNS,
    defaultSortField: 'date',
    defaultSortDir: 'desc',
  });

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError('');
    try {
      const endpoint =
        tab === 'trash' ? '/audiobooks/trashed-versions' : '/audiobooks/purged-versions';
      const resp = await apiFetch(`${API_BASE}${endpoint}`);
      if (resp.ok) {
        const data = unwrapData<{ versions?: BookVersion[] }>(await resp.json());
        setVersions(data.versions || []);
      } else {
        setVersions([]);
        setLoadError(`Failed to load versions (HTTP ${resp.status})`);
      }
    } catch (err) {
      // An expired session (ApiAuthRedirectError) lands here too; show it
      // instead of an empty list that reads as "nothing trashed".
      setVersions([]);
      setLoadError(describeRequestError(err, 'Failed to load versions'));
    } finally {
      setLoading(false);
    }
  }, [tab]);

  useEffect(() => {
    load();
  }, [load]);

  useEffect(() => {
    if (!cleanupOp || !cleanupSet) return;
    let cancelled = false;
    const key = `${cleanupOp}/${cleanupSet}`;
    getOperationResult(cleanupOp)
      .then(({ result_data }) => {
        if (cancelled) return;
        const parsed = parseTrashCleanupResult(result_data);
        setFilterRead(
          parsed
            ? { key, ids: cleanupVersionIds(parsed, cleanupSet), error: '' }
            : { key, ids: null, error: 'That cleanup run stored no result.' }
        );
      })
      .catch((err) => {
        if (cancelled) return;
        setFilterRead({
          key,
          ids: null,
          error: describeRequestError(err, "Could not read that cleanup run's result"),
        });
      });
    return () => {
      cancelled = true;
    };
  }, [cleanupOp, cleanupSet]);

  // run does one row action and shows why it failed (a refused restore, a
  // purge that gave up on a file) instead of dropping the rejection.
  const run = useCallback(
    async (what: string, action: () => Promise<unknown>) => {
      setActionError('');
      try {
        await action();
      } catch (err) {
        setActionError(
          isAuthRedirectError(err)
            ? SESSION_EXPIRED_MESSAGE
            : `${what} failed: ${err instanceof Error && err.message ? err.message : 'request failed'}`
        );
      }
      load();
    },
    [load]
  );

  const handleRestore = useCallback(
    (v: BookVersion) => run('Restore', () => restoreVersion(v.book_id, v.id)),
    [run]
  );

  const handlePurge = useCallback(
    async (v: BookVersion) => {
      if (!confirm('Permanently delete files for this version? This cannot be undone.')) return;
      await run('Purge', () => purgeVersion(v.book_id, v.id));
    },
    [run]
  );

  const handleHardDelete = useCallback(
    async (v: BookVersion) => {
      const failedNote =
        v.status === 'purge_failed'
          ? ' The files the purge could not remove stay on disk where they are.'
          : '';
      if (
        !confirm(`Remove all traces of this version? Fingerprint data will be lost.${failedNote}`)
      )
        return;
      await run('Hard delete', () => hardDeleteVersion(v.id));
    },
    [run]
  );

  const filtering = Boolean(cleanupOp && cleanupSet) && filterError === '';
  const filterWaiting = filtering && filterIds === null;
  const listed = useMemo(() => {
    if (!filtering || filterIds === null) return versions;
    const wanted = new Set(filterIds);
    return versions.filter((v) => wanted.has(v.id));
  }, [filtering, filterIds, versions]);
  // The run named these versions; the ones not on the open tab were hard
  // deleted since, or sit on the other tab. Said, so the count never looks short.
  const filterElsewhere =
    filtering && filterIds !== null && !loading && loadError === ''
      ? Math.max(0, new Set(filterIds).size - listed.length)
      : 0;
  const clearFilter = useCallback(() => {
    const next = new URLSearchParams(searchParams);
    next.delete('op');
    next.delete('set');
    setSearchParams(next, { replace: true });
  }, [searchParams, setSearchParams]);

  const sorted = sortRows(listed);

  return (
    <Box sx={{ p: 3 }}>
      <Stack
        direction="row"
        sx={{
          alignItems: 'center',
          justifyContent: 'space-between',
          mb: 2,
        }}
      >
        <Typography variant="h4">Version Management</Typography>
        <ColumnPicker
          columns={allColumns}
          isVisible={isColumnVisible}
          onToggle={toggleColumn}
          onReset={resetColumns}
        />
      </Stack>

      <Tabs
        value={tab}
        onChange={(_, v) => setTab(v)}
        variant="scrollable"
        scrollButtons="auto"
        allowScrollButtonsMobile
        sx={{ mb: 2 }}
      >
        <Tab label="Trash" value="trash" />
        <Tab label="Purged" value="purged" />
      </Tabs>

      {cleanupOp && cleanupSet && filterError !== '' && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          {filterError} Showing every version instead.
        </Alert>
      )}

      {filtering && filterIds !== null && cleanupSet !== null && (
        <Alert
          severity="info"
          sx={{ mb: 2 }}
          action={
            <Button color="inherit" size="small" onClick={clearFilter}>
              Show all
            </Button>
          }
        >
          {`Showing the ${filterIds.length.toLocaleString()} ${filterIds.length === 1 ? 'version' : 'versions'} ${CLEANUP_SET_TEXT[cleanupSet]}`}
          {filterElsewhere > 0 &&
            ` ${filterElsewhere.toLocaleString()} ${filterElsewhere === 1 ? 'is' : 'are'} not on this tab: hard deleted since, or on the other tab.`}
        </Alert>
      )}

      {actionError && (
        <Alert severity="error" sx={{ mb: 2 }} onClose={() => setActionError('')}>
          {actionError}
        </Alert>
      )}

      {loading ? (
        <Typography
          sx={{
            color: 'text.secondary',
          }}
        >
          Loading...
        </Typography>
      ) : loadError ? (
        <Alert severity="error">{loadError}</Alert>
      ) : filterWaiting ? (
        <Typography sx={{ color: 'text.secondary' }}>Loading the cleanup run...</Typography>
      ) : listed.length === 0 ? (
        <Typography
          sx={{
            color: 'text.secondary',
          }}
        >
          {filtering && versions.length > 0
            ? "None of that cleanup run's versions are on this tab."
            : tab === 'trash'
              ? 'No trashed versions.'
              : 'No purged versions.'}
        </Typography>
      ) : (
        <TableContainer component={Paper}>
          <Table size="small" sx={{ tableLayout: 'fixed' }}>
            <TableHead>
              <TableRow>
                {visibleColumns.map((col) => (
                  <ResizableHeaderCell
                    key={col.key}
                    columnKey={col.key}
                    label={col.label}
                    width={columnWidths[col.key] ?? col.defaultWidth ?? 150}
                    sortable={col.sortable}
                    sortActive={sortField === col.key}
                    sortDirection={sortDir}
                    onSort={() => handleSort(col.key)}
                    onStartResize={startResize}
                  />
                ))}
                <TableCell align="right" sx={{ width: 220 }}>
                  Actions
                </TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {sorted.map((v) => (
                <TableRow key={v.id}>
                  {visibleColumns.map((col) => (
                    <TableCell
                      key={col.key}
                      align={col.align}
                      sx={{
                        width: columnWidths[col.key],
                        maxWidth: columnWidths[col.key],
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                      }}
                    >
                      {col.render(v)}
                    </TableCell>
                  ))}
                  <TableCell align="right">
                    {v.status === 'trash' && (
                      <>
                        <Button size="small" onClick={() => handleRestore(v)}>
                          Restore
                        </Button>
                        <Button size="small" color="error" onClick={() => handlePurge(v)}>
                          Purge Now
                        </Button>
                      </>
                    )}
                    {purgeUnfinished(v.status) && (
                      // A purge that failed part way: some files are already gone,
                      // so there is no Restore. A purging row is retried by the
                      // nightly cleanup (this retries it now); a purge_failed row
                      // gave up on a file it cannot remove and is not retried
                      // nightly, so it also offers Hard Delete, which drops the
                      // record and leaves the files named in its Files cell.
                      <>
                        <Button size="small" color="error" onClick={() => handlePurge(v)}>
                          Retry Purge
                        </Button>
                        {v.status === 'purge_failed' && (
                          <Button size="small" color="error" onClick={() => handleHardDelete(v)}>
                            Hard Delete
                          </Button>
                        )}
                      </>
                    )}
                    {(v.status === 'inactive_purged' || v.status === 'blocked_for_redownload') && (
                      <Button size="small" color="error" onClick={() => handleHardDelete(v)}>
                        Hard Delete
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}
    </Box>
  );
}
