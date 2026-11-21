// file: web/src/modules/books/pages/ChapterConsolidationCard.tsx
// version: 0.1.0
// guid: 3e20f582-f045-4e1b-aa65-3eebb33f8897
// last-edited: 2026-11-21

// A books system widget (placement "system", 07 §7.11): chapter consolidation (F9-04 slice 1).

import { useState, useCallback } from 'react';
import type { Dispatch, SetStateAction } from 'react';
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  CardHeader,
  Checkbox,
  Chip,
  CircularProgress,
  Collapse,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  FormControlLabel,
  List,
  ListItem,
  ListItemText,
  Stack,
  Switch,
  TextField,
  Typography,
} from '@mui/material';
import * as api from '../../../services/api';

type ChapterJobId = 'scan-chapter-groups' | 'merge-chapter-groups';

const chapterStatusColor: Record<
  NonNullable<api.ChapterGroup['status']>,
  'default' | 'success' | 'warning' | 'error' | 'info'
> = {
  would_merge: 'info',
  would_skip: 'warning',
  blocked: 'warning',
  drifted: 'warning',
  selection_mismatch: 'warning',
  merged: 'success',
  partial: 'warning',
  failed: 'error',
};

/**
 * Detects sequential chapter files that were imported as separate books and
 * merges them into the chapter-01 book. Both actions are async maintenance
 * jobs: the card starts one, follows the operation to a terminal status, then
 * renders the structured result the job stored on the operation.
 *
 * A real merge is never one click: it needs a merge PREVIEW (a dry run, which
 * also runs the iTunes guard and the carry checks -- a scan does not count),
 * and a confirm dialog that names the counts. The real merge sends exactly the
 * previewed groups (minus any the operator unticked) with their fingerprints and
 * the preview's own params -- never the form as edited since -- and the server
 * merges only those, skipping any group that changed after the preview.
 */
export function ChapterConsolidationCard() {
  const [running, setRunning] = useState<ChapterJobId | null>(null);
  const [progress, setProgress] = useState<string | null>(null);
  const [dryRun, setDryRun] = useState(true);
  const [minFiles, setMinFiles] = useState('2');
  const [maxPerFile, setMaxPerFile] = useState('600');
  const [pathPrefix, setPathPrefix] = useState('');
  const [result, setResult] = useState<api.ChapterGroupsResult | null>(null);
  const [expanded, setExpanded] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  // Selection is OPT-IN: nothing is ticked until the operator ticks it (or
  // uses "Select all", which never includes a low-confidence group).
  const [chosen, setChosen] = useState<Set<string>>(new Set());
  const [openRows, setOpenRows] = useState<Set<string>>(new Set());
  const [error, setError] = useState<string | null>(null);

  const params = useCallback((): api.ChapterGroupsParams => {
    const p: api.ChapterGroupsParams = {};
    const mf = Number.parseInt(minFiles, 10);
    const mp = Number.parseInt(maxPerFile, 10);
    if (Number.isFinite(mf) && mf > 0) p.min_files = mf;
    if (Number.isFinite(mp) && mp > 0) p.max_per_file_duration = mp;
    if (pathPrefix.trim()) p.path_prefix = pathPrefix.trim();
    return p;
  }, [minFiles, maxPerFile, pathPrefix]);

  const runJob = useCallback(
    async (jobId: ChapterJobId, jobDryRun: boolean, jobParams: api.ChapterGroupsParams) => {
      setRunning(jobId);
      setProgress('Starting…');
      setError(null);
      setResult(null);
      setChosen(new Set());
      setOpenRows(new Set());
      try {
        const { operation_id: opId } = await api.runMaintenanceJob(jobId, jobDryRun, {
          ...jobParams,
        });
        if (!opId) throw new Error('The server did not return an operation id');
        const op = await api.pollOperation(opId, (o) => {
          setProgress(o.total > 0 ? `${o.progress} / ${o.total} groups` : o.status);
        });
        if (op.status !== 'completed') {
          throw new Error(op.error_message || `Operation ${opId} ended ${op.status}`);
        }
        const { result_data } = await api.getOperationResult(opId);
        if (!result_data || typeof result_data !== 'object') {
          throw new Error(`Operation ${opId} finished without a result`);
        }
        setResult(result_data as api.ChapterGroupsResult);
      } catch (e) {
        setError(e instanceof Error ? e.message : 'Chapter consolidation failed');
      } finally {
        setRunning(null);
        setProgress(null);
      }
    },
    []
  );

  // A real merge needs a merge PREVIEW to confirm against. A scan does not
  // count (it runs neither the iTunes guard nor the carry checks), and a
  // result that was itself a real merge does not either.
  const preview = result && result.job === 'merge-chapter-groups' && result.dry_run ? result : null;
  // Only groups the preview would merge, and the operator ticked, are sent
  // -- blocked / would_skip groups never are.
  const mergeable = preview
    ? preview.groups.filter((g) => g.status === 'would_merge' && g.fingerprint)
    : [];
  const selectedGroups = mergeable.filter((g) => chosen.has(g.primary_book_id));
  const bulkSelectable = mergeable.filter((g) => g.confidence !== 'low');
  const previewSources = selectedGroups.reduce((n, g) => n + g.source_book_ids.length, 0);

  const handleMergeClick = () => {
    if (dryRun) {
      void runJob('merge-chapter-groups', true, params());
    } else {
      setConfirmOpen(true);
    }
  };

  const handleConfirmMerge = () => {
    setConfirmOpen(false);
    if (!preview) return;
    // The PREVIEW's params, not the form's current values, and exactly the
    // reviewed groups with their fingerprints.
    const { dry_run: _ignored, groups: _prev, ...previewParams } = preview.params;
    void _ignored;
    void _prev;
    void runJob('merge-chapter-groups', false, {
      ...previewParams,
      groups: selectedGroups.map((g) => ({
        primary_book_id: g.primary_book_id,
        book_ids: g.book_ids,
        fingerprint: g.fingerprint ?? '',
        // A low group can only be ticked one by one; that tick is the
        // acknowledgement the server requires to merge it.
        ...(g.confidence === 'low' ? { allow_low_confidence: true } : {}),
      })),
    });
  };

  const toggleIn = (setter: Dispatch<SetStateAction<Set<string>>>) => (primaryId: string) =>
    setter((prev) => {
      const next = new Set(prev);
      if (next.has(primaryId)) next.delete(primaryId);
      else next.add(primaryId);
      return next;
    });
  const toggleGroup = toggleIn(setChosen);
  const toggleRow = toggleIn(setOpenRows);
  const groups = result?.groups ?? [];
  const isMergeResult = result?.job === 'merge-chapter-groups';

  return (
    <Card variant="outlined" sx={{ mb: 2 }}>
      <CardHeader
        title="Chapter Consolidation"
        subheader="Detect and merge sequential chapter files (01 - Title.mp3, 02 - Title.mp3 …) into a single book record. Merges are undoable."
      />
      <CardContent>
        {error && (
          <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>
            {error}
          </Alert>
        )}

        <Stack direction="row" spacing={2} sx={{ flexWrap: 'wrap', mb: 2, rowGap: 1 }}>
          <TextField
            size="small"
            type="number"
            label="Min files"
            value={minFiles}
            onChange={(e) => setMinFiles(e.target.value)}
            sx={{ width: 110 }}
          />
          <TextField
            size="small"
            type="number"
            label="Max seconds per file"
            value={maxPerFile}
            onChange={(e) => setMaxPerFile(e.target.value)}
            sx={{ width: 170 }}
          />
          <TextField
            size="small"
            label="Path prefix (optional)"
            value={pathPrefix}
            onChange={(e) => setPathPrefix(e.target.value)}
            sx={{ minWidth: 220, flexGrow: 1 }}
          />
        </Stack>

        <Stack direction="row" spacing={2} sx={{ flexWrap: 'wrap', mb: 2, rowGap: 1 }}>
          <Button
            variant="outlined"
            startIcon={
              running === 'scan-chapter-groups' ? <CircularProgress size={14} /> : undefined
            }
            disabled={running !== null}
            onClick={() => void runJob('scan-chapter-groups', true, params())}
          >
            {running === 'scan-chapter-groups' ? 'Scanning…' : 'Scan for Chapter Groups'}
          </Button>

          <FormControlLabel
            control={
              <Switch
                checked={dryRun}
                onChange={(e) => setDryRun(e.target.checked)}
                size="small"
                disabled={running !== null}
              />
            }
            label="Dry Run"
          />

          <Button
            variant="contained"
            color={dryRun ? 'info' : 'warning'}
            startIcon={
              running === 'merge-chapter-groups' ? (
                <CircularProgress size={14} color="inherit" />
              ) : undefined
            }
            disabled={running !== null || (!dryRun && selectedGroups.length === 0)}
            onClick={handleMergeClick}
          >
            {running === 'merge-chapter-groups'
              ? 'Merging…'
              : dryRun
                ? 'Preview Merge'
                : 'Merge Chapter Groups…'}
          </Button>
        </Stack>

        {!dryRun && !preview && running === null && (
          <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 1 }}>
            Run a scan or a preview first; a real merge is confirmed against its counts.
          </Typography>
        )}

        {progress && (
          <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
            {progress}
          </Typography>
        )}

        {result && !isMergeResult && (
          <Typography variant="body2" sx={{ mb: 1 }} data-testid="chapter-summary">
            Found <strong>{result.groups_found}</strong> group(s) affecting{' '}
            <strong>{result.total_books_affected}</strong> book record(s).
            {(result.groups_blocked ?? 0) > 0 && (
              <>
                {' '}
                <strong>{result.groups_blocked}</strong> more blocked (reasons listed).
              </>
            )}
          </Typography>
        )}

        {result && isMergeResult && (
          <Typography variant="body2" sx={{ mb: 1 }} data-testid="chapter-summary">
            {result.dry_run ? '[Dry run] Would merge' : 'Merged'}{' '}
            <strong>{result.books_merged}</strong> book record(s) across{' '}
            <strong>{result.groups_found}</strong> group(s).
            {result.books_skipped > 0 && (
              <>
                {' '}
                Skipped <strong>{result.books_skipped}</strong>.
              </>
            )}
            {result.groups_failed > 0 && (
              <>
                {' '}
                <strong>{result.groups_failed}</strong> group(s) failed.
              </>
            )}
          </Typography>
        )}

        {groups.length > 0 && (
          <>
            <Button size="small" onClick={() => setExpanded((v) => !v)} sx={{ mb: 1 }}>
              {expanded ? 'Hide groups' : `Show ${groups.length} group(s)`}
            </Button>
            {preview && bulkSelectable.length > 0 && (
              <Button
                size="small"
                onClick={() => setChosen(new Set(bulkSelectable.map((g) => g.primary_book_id)))}
                sx={{ mb: 1, ml: 1 }}
              >
                Select all ({bulkSelectable.length}, low confidence excluded)
              </Button>
            )}
            {preview && selectedGroups.length > 0 && (
              <Button size="small" onClick={() => setChosen(new Set())} sx={{ mb: 1, ml: 1 }}>
                Clear selection
              </Button>
            )}
            <Collapse in={expanded}>
              <List dense disablePadding>
                {groups.map((g) => (
                  <Box key={g.primary_book_id}>
                    <ListItem disableGutters>
                      {preview && g.status === 'would_merge' && (
                        <Checkbox
                          size="small"
                          checked={chosen.has(g.primary_book_id)}
                          onChange={() => toggleGroup(g.primary_book_id)}
                          slotProps={{ input: { 'aria-label': `Include ${g.primary_book_id}` } }}
                        />
                      )}
                      <ListItemText
                        primary={g.common_title || '(unknown title)'}
                        secondary={
                          `proposed title "${g.common_title || '(none)'}" · ` +
                          `${g.file_count} files · ${Math.round(g.total_duration / 60)} min total · ${g.directory}` +
                          (g.confidence ? ` · ${g.confidence} confidence` : '') +
                          (g.gaps && g.gaps.length > 0 ? ` · missing ${g.gaps.join(', ')}` : '') +
                          (g.title_action === 'kept' && g.primary_title
                            ? ` · keeps title "${g.primary_title}"`
                            : '') +
                          (g.metadata_fills && g.metadata_fills.length > 0
                            ? ` · fills empty ${g.metadata_fills.join(', ')}`
                            : '') +
                          (g.blockers && g.blockers.length > 0
                            ? ` · blocked: ${g.blockers.join('; ')}`
                            : '') +
                          (g.errors && g.errors.length > 0 ? ` · ${g.errors.join('; ')}` : '')
                        }
                      />
                      {g.status && (
                        <Chip
                          size="small"
                          color={chapterStatusColor[g.status]}
                          label={g.status.replace('_', ' ')}
                          sx={{ ml: 1 }}
                        />
                      )}
                      <Chip size="small" label={`${g.file_count} files`} sx={{ ml: 1 }} />
                      {g.member_titles && g.member_titles.length > 0 && (
                        <Button
                          size="small"
                          onClick={() => toggleRow(g.primary_book_id)}
                          aria-label={`Members of ${g.primary_book_id}`}
                          sx={{ ml: 1 }}
                        >
                          {openRows.has(g.primary_book_id) ? 'Hide members' : 'Members'}
                        </Button>
                      )}
                    </ListItem>
                    {openRows.has(g.primary_book_id) && g.member_titles && (
                      <Box
                        component="ol"
                        data-testid={`members-${g.primary_book_id}`}
                        sx={{ mt: 0, mb: 1, pl: 6, typography: 'caption' }}
                      >
                        {g.member_titles.map((title, i) => (
                          <li key={g.book_ids[i] ?? i}>
                            {`#${g.index_labels?.[i] ?? i + 1} · "${title}" · ${
                              g.member_durations?.[i]
                                ? `${Math.round((g.member_durations[i] ?? 0) / 60)} min`
                                : 'duration unknown'
                            } · ${g.member_files?.[i] ?? ''}`}
                          </li>
                        ))}
                      </Box>
                    )}
                  </Box>
                ))}
              </List>
            </Collapse>
          </>
        )}
      </CardContent>

      <Dialog open={confirmOpen} onClose={() => setConfirmOpen(false)}>
        <DialogTitle>Merge chapter groups?</DialogTitle>
        <DialogContent>
          <DialogContentText>
            The last preview found <strong>{preview?.groups_found ?? 0}</strong> group(s):{' '}
            <strong>{previewSources}</strong> chapter book record(s) would be folded into their
            chapter-01 book. The server detects again when the merge runs, so the final count can
            differ if the library changed. Each merge writes an undo journal.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmOpen(false)}>Cancel</Button>
          <Button color="warning" variant="contained" onClick={handleConfirmMerge}>
            Merge {previewSources} record(s)
          </Button>
        </DialogActions>
      </Dialog>
    </Card>
  );
}
