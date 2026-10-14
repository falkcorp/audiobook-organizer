// file: web/src/components/review/AIScansPanel.tsx
// version: 1.2.0
// guid: b84f1c2d-7e09-4a63-95d8-1f3a6c0e5b72
// last-edited: 2026-10-13

/**
 * The Authors & series lane's AI scans sub-view: a rail, the scan history, and the
 * open scan's results.
 *
 * THE HISTORY IS A LIST, NOT A DRAWER. The old tab hid it behind a "Scan History"
 * button, so a nightly scan that had been superseded was a hunt. Every scan is on
 * screen here, newest first, each with its date, mode and status, and a
 * "superseded" marker where the server says the list was replaced by a newer run.
 *
 * FOUR STATES, TWICE. The history and the open scan's results each render
 * loading, error (with Retry), empty and populated differently; so does a scan
 * that has no results to read yet (running, failed, canceled), which is none of
 * them. None is only a console line.
 *
 * COUNTS CLICK THROUGH. The agreement chips (All / Agreed / Groups Only / Full
 * Only / Disagreed) are counted from the same array the rows come from, so each
 * filters to exactly the number it shows.
 *
 * ONLY MERGE ROWS ARE APPLIED HERE. A merge row says which author it keeps and
 * which it folds in ("keep X, merge Y, Z", with names, the keep first) and has a
 * checkbox; Apply selected merges those authors (locked author names stay where
 * they are) and marks the rows that merged. Rename, alias, split, reclassify and
 * skip rows are read-only, with a link to the Authors view focused on their
 * group. A disagreed row shows both phases' answers, whichever it is.
 *
 * Starting and cancelling a scan are commands in the lane menu (ReviewWorkspace),
 * which report through the notification bell. This panel has the Realtime / Batch
 * selector they read (two labelled buttons, so both options and their costs are
 * on screen at once), and a Refresh button, because nothing here polls.
 *
 * There are no keyboard shortcuts in this sub-view. The Authors and Series
 * handlers are unbound while it shows, so `?` falls through to the app's own
 * shortcut dialog.
 */

import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Checkbox,
  Chip,
  CircularProgress,
  IconButton,
  LinearProgress,
  Link,
  List,
  ListItemButton,
  Paper,
  Stack,
  ToggleButton,
  ToggleButtonGroup,
  Tooltip,
  Typography,
} from '@mui/material';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import RefreshIcon from '@mui/icons-material/Refresh';
import type { AIScan, AIScanResult, SuggestionRoles } from '../../services/api';
import { AuthorsSubViewSwitch } from './AuthorsSubViewSwitch';
import type { AuthorsLane } from './lanes/useAuthorsLane';
import { AGREEMENT_FILTERS, isTerminalScan, type AIScansLane } from './lanes/useAIScans';

const STATUS_COLOR: Record<string, 'default' | 'success' | 'error' | 'warning' | 'info'> = {
  complete: 'success',
  failed: 'error',
  canceled: 'warning',
  superseded: 'info',
};

function RoleLines({ roles }: { roles: SuggestionRoles }) {
  const rows: Array<[string, string | undefined, string | undefined]> = [
    ['Author', roles.author?.name, roles.author?.reason],
    ['Narrator', roles.narrator?.name, roles.narrator?.reason],
    ['Publisher', roles.publisher?.name, roles.publisher?.reason],
  ];
  return (
    <Box sx={{ ml: 5, mt: 0.5 }} data-testid="ai-result-roles">
      {rows.map(([label, name, reason]) =>
        name ? (
          <Box key={label} sx={{ mb: 0.5 }}>
            <Typography variant="body2" component="span" sx={{ fontWeight: 500 }}>
              {label}: {name}
            </Typography>
            {label === 'Author' && roles.author?.variants && roles.author.variants.length > 0 && (
              <Typography variant="caption" sx={{ display: 'block', color: 'text.secondary' }}>
                Variants: {roles.author.variants.join(', ')}
              </Typography>
            )}
            {reason && (
              <Typography variant="caption" sx={{ display: 'block', fontStyle: 'italic', color: 'text.secondary' }}>
                &ldquo;{reason}&rdquo;
              </Typography>
            )}
          </Box>
        ) : null
      )}
    </Box>
  );
}

function ScanEntry({ s, open, onOpen }: { s: AIScan; open: boolean; onOpen: () => void }) {
  return (
    <ListItemButton
      selected={open}
      onClick={onOpen}
      data-testid={`ai-scan-${s.id}`}
      aria-current={open ? 'true' : undefined}
      sx={{ display: 'block', borderBottom: 1, borderColor: 'divider' }}
    >
      <Stack direction="row" spacing={1} useFlexGap sx={{ alignItems: 'center', flexWrap: 'wrap' }}>
        <Typography variant="body2" sx={{ fontWeight: 'bold' }}>
          Scan #{s.id}
        </Typography>
        <Chip size="small" variant="outlined" label={s.mode} />
        {s.status === 'superseded' ? (
          <Chip size="small" color="info" label="superseded" data-testid={`ai-scan-superseded-${s.id}`} />
        ) : (
          <Chip size="small" color={STATUS_COLOR[s.status] ?? 'default'} label={s.status} />
        )}
      </Stack>
      <Typography variant="caption" sx={{ color: 'text.secondary' }}>
        {new Date(s.created_at).toLocaleString()} · scanned {s.author_count} authors
      </Typography>
      {/* Visible text, not a tooltip: a chip is not focusable, so a hover-only
          explanation is out of a keyboard user's reach. */}
      {s.status === 'superseded' && (
        <Typography variant="caption" sx={{ display: 'block', color: 'text.secondary' }} data-testid={`ai-scan-replaced-by-${s.id}`}>
          Replaced by scan #{s.superseded_by ?? '?'} before anything was applied from it
        </Typography>
      )}
    </ListItemButton>
  );
}

/** `/review?lane=authors&author=<id>`: the Authors view focused on the group that author is in. */
const authorsViewHref = (authorId: number) => `/review?lane=authors&author=${authorId}`;

/** An author by name, or by id when the name is gone (merged away since the scan). */
const nameOf = (names: Readonly<Record<number, string>>, id: number) => names[id] ?? `author #${id} (no longer exists)`;

const PHASE_LABEL: Record<string, string> = { groups_scan: 'Groups scan', full_scan: 'Full scan' };

/** What a merge row will do, naming the keep first. */
function MergePlan({ r, names }: { r: AIScanResult; names: Readonly<Record<number, string>> }) {
  const { suggestion } = r;
  const keep = suggestion.keep_id ?? 0;
  if (keep <= 0) {
    return (
      <Typography variant="caption" sx={{ display: 'block', color: 'warning.main' }} data-testid={`ai-result-plan-${r.id}`}>
        No keep author could be chosen for this merge, so it cannot be applied here.{' '}
        <Link href={authorsViewHref((suggestion.author_ids ?? [])[0] ?? 0)} target="_blank" rel="noopener">
          Review it in the Authors view
        </Link>
      </Typography>
    );
  }
  const merged = (suggestion.author_ids ?? []).filter((id) => id !== keep);
  return (
    <Typography variant="body2" sx={{ display: 'block' }} data-testid={`ai-result-plan-${r.id}`}>
      keep <strong>{nameOf(names, keep)}</strong>, merge{' '}
      {merged.length > 0 ? merged.map((id) => nameOf(names, id)).join(', ') : 'nothing'}
    </Typography>
  );
}

function ResultCard({ r, lane }: { r: AIScanResult; lane: AIScansLane }) {
  const { suggestion } = r;
  const isMerge = suggestion.action === 'merge';
  const pair = lane.pairOf(r);
  const focusId = suggestion.keep_id || (suggestion.author_ids ?? [])[0] || 0;
  return (
    <Card sx={{ mb: 1, opacity: r.applied ? 0.5 : 1 }} variant="outlined" data-testid={`ai-result-${r.id}`}>
      <CardContent sx={{ py: 1, '&:last-child': { pb: 1 } }}>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
          <Checkbox
            {...lane.selection.checkboxProps(r.id)}
            slotProps={{ input: { 'aria-label': `Select result ${r.id}` } }}
            size="small"
          />
          <Chip
            label={r.agreement}
            size="small"
            color={r.agreement === 'agreed' ? 'success' : r.agreement === 'disagreed' ? 'error' : 'default'}
          />
          <Chip
            label={suggestion.action}
            size="small"
            variant="outlined"
            color={
              isMerge
                ? 'primary'
                : suggestion.action === 'rename'
                  ? 'warning'
                  : suggestion.action === 'alias'
                    ? 'info'
                    : 'default'
            }
          />
          <Chip
            label={suggestion.confidence}
            size="small"
            variant="outlined"
            color={suggestion.confidence === 'high' ? 'success' : suggestion.confidence === 'medium' ? 'warning' : 'error'}
          />
          <Box sx={{ flex: 1 }}>
            <Typography variant="body2" sx={{ fontWeight: 'bold' }}>
              {suggestion.canonical_name}
            </Typography>
            <Typography variant="caption" sx={{ color: 'text.secondary' }}>
              {suggestion.reason}
            </Typography>
          </Box>
          {r.applied && <Chip label="Applied" size="small" color="info" />}
        </Box>
        {isMerge && (
          <Box sx={{ ml: 5, mt: 0.5 }}>
            <MergePlan r={r} names={lane.authorNames} />
          </Box>
        )}
        {!isMerge && (
          <Typography
            variant="caption"
            sx={{ display: 'block', ml: 5, mt: 0.5, color: 'text.secondary' }}
            data-testid={`ai-result-readonly-${r.id}`}
          >
            Read-only here: only merges are applied from this view.{' '}
            {focusId > 0 && (
              <Link href={authorsViewHref(focusId)} target="_blank" rel="noopener" data-testid={`ai-result-authors-link-${r.id}`}>
                Review it in the Authors view
              </Link>
            )}
          </Typography>
        )}
        {r.agreement === 'disagreed' && (
          <Typography
            variant="caption"
            sx={{ display: 'block', ml: 5, mt: 0.5, color: 'text.secondary' }}
            data-testid={`ai-result-pair-${r.id}`}
          >
            {pair
              ? [r, pair]
                  .sort((x, y) => (x.suggestion.source === 'groups_scan' ? -1 : y.suggestion.source === 'groups_scan' ? 1 : 0))
                  .map(
                    (x) =>
                      `${PHASE_LABEL[x.suggestion.source] ?? x.suggestion.source} said ${x.suggestion.action} "${x.suggestion.canonical_name}"`
                  )
                  .join(' · ')
              : `${PHASE_LABEL[suggestion.source] ?? suggestion.source} said ${suggestion.action}; the other phase disagreed.`}
          </Typography>
        )}
        {suggestion.roles && <RoleLines roles={suggestion.roles} />}
      </CardContent>
    </Card>
  );
}

export function AIScansPanel({
  authors,
  ai,
  onApply,
}: {
  authors: AuthorsLane;
  ai: AIScansLane;
  /**
   * Starts the apply and reports it (ReviewWorkspace wraps `ai.applySelected` in
   * its startJob, so the bell and a toast carry it). Without one, the panel
   * applies silently and the notice below is the report.
   */
  onApply?: () => void | Promise<void>;
}) {
  const { scan, selection } = ai;
  const running = scan !== null && !isTerminalScan(scan.status);

  return (
    <Box sx={{ flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column' }} data-testid="ai-scans-panel">
      <Box
        data-testid="ai-scans-rail"
        sx={{ borderBottom: 1, borderColor: 'divider', px: 2, py: 1, display: 'flex', flexDirection: 'column', gap: 1 }}
      >
        <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap', alignItems: 'center' }}>
          <AuthorsSubViewSwitch view={authors.view} onChange={authors.setView} />
          <Typography variant="body2" sx={{ color: 'text.secondary', flexGrow: 1 }}>
            AI author scans, including the nightly ones. Start or cancel a scan from the Authors &amp; series menu;
            progress shows in the notification bell.
          </Typography>
          <ToggleButtonGroup
            size="small"
            exclusive
            value={ai.mode}
            // Clicking the selected button yields null; the selector always has one.
            onChange={(_, v: AIScansLane['mode'] | null) => v && ai.setMode(v)}
            aria-label="Scan mode"
            data-testid="ai-scan-mode"
          >
            <ToggleButton value="realtime" data-testid="ai-scan-mode-realtime">
              Realtime (faster, more expensive)
            </ToggleButton>
            <ToggleButton value="batch" data-testid="ai-scan-mode-batch">
              Batch (cheaper, hours)
            </ToggleButton>
          </ToggleButtonGroup>
          <Tooltip title="Refresh">
            <span>
              <IconButton onClick={ai.refresh} aria-label="Refresh">
                <RefreshIcon />
              </IconButton>
            </span>
          </Tooltip>
        </Stack>
      </Box>
      {ai.refreshing && <LinearProgress data-testid="ai-scans-refreshing" />}

      {ai.notice && (
        <Alert severity={ai.notice.severity} data-testid="ai-scans-notice" sx={{ mx: 2, my: 1 }} onClose={ai.dismissNotice}>
          {ai.notice.text}
        </Alert>
      )}

      <Box
        sx={{
          flex: 1,
          minHeight: 0,
          display: 'grid',
          gridTemplateColumns: { xs: '1fr', md: 'minmax(260px, 340px) 1fr' },
          gap: 2,
          px: 2,
          py: 1,
          overflowY: 'auto',
        }}
      >
        {/* ---- history ---- */}
        <Box data-testid="ai-history" sx={{ minWidth: 0 }}>
          <Typography variant="subtitle2" sx={{ mb: 1 }}>
            Scan history
          </Typography>
          {ai.historyStatus === 'loading' && (
            <Box sx={{ display: 'flex', justifyContent: 'center', py: 3 }} data-testid="ai-history-loading">
              <CircularProgress size={28} />
            </Box>
          )}
          {ai.historyStatus === 'error' && (
            <Alert
              severity="error"
              data-testid="ai-history-error"
              action={
                <Button color="inherit" size="small" onClick={ai.retryHistory}>
                  Retry
                </Button>
              }
            >
              Could not load the scan history: {ai.historyError}
            </Alert>
          )}
          {ai.historyStatus === 'empty' && (
            <Paper sx={{ p: 2, textAlign: 'center' }} data-testid="ai-history-empty">
              <Typography variant="body2" sx={{ color: 'text.secondary' }}>
                No scans yet. Start one from the Authors &amp; series menu.
              </Typography>
            </Paper>
          )}
          {ai.scans.length > 0 && (
            <List disablePadding data-testid="ai-history-list" sx={{ border: 1, borderColor: 'divider', borderRadius: 1 }}>
              {ai.scans.map((s) => (
                <ScanEntry key={s.id} s={s} open={scan?.id === s.id} onOpen={() => ai.selectScan(s.id)} />
              ))}
            </List>
          )}
        </Box>

        {/* ---- the open scan ---- */}
        <Box data-testid="ai-results" sx={{ minWidth: 0 }}>
          {scan === null && (
            <Paper sx={{ p: 4, textAlign: 'center' }} data-testid="ai-no-scan">
              <Typography variant="body1" sx={{ color: 'text.secondary' }}>
                Pick a scan from the history to see what it found.
              </Typography>
            </Paper>
          )}

          {scan && running && (
            <Alert severity="info" data-testid="ai-scan-running" sx={{ mb: 2 }}>
              Scan #{scan.id} is {scan.status.replaceAll('_', ' ')}. It has no results yet; its progress is in the
              notification bell. Refresh when it finishes.
            </Alert>
          )}
          {scan?.status === 'failed' && (
            <Alert severity="error" data-testid="ai-scan-failed" sx={{ mb: 2 }}>
              Scan #{scan.id} failed. Start a new scan from the Authors &amp; series menu.
            </Alert>
          )}
          {scan?.status === 'canceled' && (
            <Alert severity="warning" data-testid="ai-scan-canceled" sx={{ mb: 2 }}>
              Scan #{scan.id} was canceled.
            </Alert>
          )}
          {ai.superseded && scan && (
            <Alert severity="info" data-testid="ai-scan-superseded" sx={{ mb: 2 }}>
              Scan #{scan.id} was superseded by scan #{scan.superseded_by ?? '?'} — a newer nightly run — before
              anything was applied from it. Its results are shown read-only; apply from scan #
              {scan.superseded_by ?? '?'} instead.
            </Alert>
          )}

          {ai.resultsStatus === 'loading' && (
            <Box sx={{ display: 'flex', justifyContent: 'center', py: 4 }} data-testid="ai-results-loading">
              <CircularProgress />
            </Box>
          )}
          {ai.resultsStatus === 'error' && (
            <Alert
              severity="error"
              data-testid="ai-results-error"
              action={
                <Button color="inherit" size="small" onClick={ai.retryResults}>
                  Retry
                </Button>
              }
            >
              Could not load the results of scan #{scan?.id}: {ai.resultsError}
            </Alert>
          )}
          {ai.resultsStatus === 'empty' && scan && (
            <Paper sx={{ p: 4, textAlign: 'center' }} data-testid="ai-results-empty">
              <CheckCircleIcon sx={{ fontSize: 48, color: 'success.main', mb: 1 }} />
              <Typography variant="h6">No duplicate authors found</Typography>
              <Typography variant="body2" sx={{ color: 'text.secondary' }}>
                Scan #{scan.id} finished and suggested nothing.
              </Typography>
            </Paper>
          )}

          {ai.resultsStatus === 'populated' && (
            <>
              <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap', alignItems: 'center', mb: 2 }}>
                {AGREEMENT_FILTERS.map(({ value, label }) => (
                  <Chip
                    key={value}
                    size="small"
                    color={ai.filter === value ? 'primary' : 'default'}
                    variant={ai.filter === value ? 'filled' : 'outlined'}
                    label={`${label} (${ai.counts[value]})`}
                    aria-pressed={ai.filter === value}
                    data-testid={`ai-agreement-${value}`}
                    onClick={() => ai.setFilter(value)}
                  />
                ))}
              </Stack>

              {selection.selectedCount > 0 && !ai.superseded && (
                <Paper
                  elevation={4}
                  data-testid="ai-apply-bar"
                  sx={{
                    position: 'sticky',
                    bottom: 16,
                    zIndex: 10,
                    p: 1.5,
                    mb: 2,
                    display: 'flex',
                    alignItems: 'center',
                    gap: 2,
                    borderRadius: 2,
                  }}
                >
                  <Button variant="contained" onClick={() => void (onApply ? onApply() : ai.applySelected().catch(() => undefined))} disabled={ai.applying}>
                    Apply selected ({selection.selectedCount})
                  </Button>
                  <Button variant="outlined" size="small" onClick={selection.clear} disabled={ai.applying}>
                    Clear selection
                  </Button>
                </Paper>
              )}

              {ai.filtered.length > 0 ? (
                <>
                  <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mb: 1 }}>
                    <Checkbox
                      size="small"
                      checked={selection.header.checked}
                      indeterminate={selection.header.indeterminate}
                      disabled={selection.header.disabled}
                      onChange={selection.togglePage}
                      slotProps={{ input: { 'aria-label': `Select all ${ai.filtered.filter(ai.isApplicable).length} mergeable results shown` } }}
                    />
                    <Typography variant="caption" sx={{ color: 'text.secondary' }}>
                      Select all mergeable results shown · Shift-click a checkbox to select a range
                    </Typography>
                  </Box>
                  {ai.filtered.map((r) => (
                    <ResultCard key={r.id} r={r} lane={ai} />
                  ))}
                </>
              ) : (
                <Paper sx={{ p: 3, textAlign: 'center' }} data-testid="ai-results-filter-empty">
                  <Typography variant="body1">No results match this filter.</Typography>
                  <Button size="small" onClick={() => ai.setFilter('all')}>
                    Show all results
                  </Button>
                </Paper>
              )}
            </>
          )}
        </Box>
      </Box>
    </Box>
  );
}
