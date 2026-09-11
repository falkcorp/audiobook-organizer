// file: web/src/pages/Dashboard.tsx
// version: 1.16.0
// guid: 2f3a4b5c-6d7e-8f9a-0b1c-2d3e4f5a6b7c
// last-edited: 2026-09-11

import { useState, useEffect, useCallback, useRef } from 'react';
import { useNavigate } from 'react-router-dom';
import { AnnouncementBanner } from '../components/AnnouncementBanner';
import {
  Box,
  Typography,
  Grid,
  Paper,
  LinearProgress,
  CircularProgress,
  Button,
  Stack,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Alert,
  AlertTitle,
  Card,
  CardContent,
  List,
  ListItem,
  ListItemText,
  Chip,
  CardActionArea,
  Checkbox,
  FormControlLabel,
  Skeleton,
  Tooltip,
} from '@mui/material';
import {
  LibraryBooks as LibraryBooksIcon,
  Folder as FolderIcon,
  CheckCircle as CheckCircleIcon,
  Error as ErrorIcon,
  Storage as StorageIcon,
  Person as PersonIcon,
  MenuBook as MenuBookIcon,
  Warning as WarningIcon,
  PauseCircleOutlined as PauseCircleOutlineIcon,
} from '@mui/icons-material';
import * as api from '../services/api';
import type { ChipColor } from '../utils/activityTagColors';
import { useOperationsStore } from '../stores/useOperationsStore';

interface SystemStats {
  library_books: number;
  import_books: number;
  total_books: number;
  total_files: number;
  total_authors: number;
  total_series: number;
  import_paths: number;
  library_size_gb: number;
  import_size_gb: number;
  total_size_gb: number;
  disk_used_gb: number;
  disk_total_gb: number;
  disk_usage_percent: number;
}

/**
 * How a recent operation is rendered. `stopped` was added on 2026-09-07 when
 * this panel was repointed from the retired v1 operations keyspace to v2: v2
 * has statuses v1 never had (`canceled`, `waiting_deps`, and four
 * `interrupted_*` variants), and collapsing them into `running` was both wrong
 * on screen and expensive — see `live` below.
 */
type RecentOperationStatus = 'success' | 'error' | 'running' | 'stopped';

interface RecentOperation {
  id: string;
  type: string;
  status: RecentOperationStatus;
  /**
   * Whether the operation is still going to do anything, taken from
   * `completed_at == null` rather than from `status`.
   *
   * This is deliberate, and it mirrors the backend: `ListOperationsV2Since`
   * keys liveness on completed_at's nullness for the same reason — a status
   * list has to be updated every time a terminal state is added, and silently
   * misreports until someone remembers. Every terminal AND every paused v2
   * status stamps completed_at at its write site, and
   * `RepairOpsV2MissingCompletedAt` exists to keep that true, so null means
   * genuinely in flight: queued, waiting on deps, or running.
   */
  live: boolean;
  message: string;
  timestamp: string;
}

/** Human-readable one-liner for an error surfaced in the UI. */
const describeError = (err: unknown): string =>
  err instanceof Error ? err.message : String(err);

export function Dashboard() {
  const navigate = useNavigate();
  // Each loader owns a value AND an error. A value is null until its request
  // has succeeded once, and a FAILED request leaves the value alone: the tile
  // keeps the last count the server confirmed and says it could not refresh.
  // Until 2026-09-11 every catch below wrote 0 into the value instead, so a
  // backend blip rendered "0 authors, 0 series, 0 imported" — which reads as
  // "the library is empty", not "the request failed" (WEB-03).
  const [stats, setStats] = useState<SystemStats | null>(null);
  const [statsError, setStatsError] = useState<string | null>(null);
  const [authorCount, setAuthorCount] = useState<number | null>(null);
  const [authorsError, setAuthorsError] = useState<string | null>(null);
  const [seriesCount, setSeriesCount] = useState<number | null>(null);
  const [seriesError, setSeriesError] = useState<string | null>(null);
  const [importedCount, setImportedCount] = useState<number | null>(null);
  const [importedError, setImportedError] = useState<string | null>(null);
  const [operations, setOperations] = useState<RecentOperation[] | null>(null);
  const [brokenFileCount, setBrokenFileCount] = useState<number | null>(null);
  const [actionNotice, setActionNotice] = useState<string | null>(null);
  const [organizeDialogOpen, setOrganizeDialogOpen] = useState(false);
  const [organizeInProgress, setOrganizeInProgress] = useState(false);
  const [syncITunesFirst, setSyncITunesFirst] = useState(true);
  const [scanInProgress, setScanInProgress] = useState(false);

  // Ref for auto-refresh interval
  const autoRefreshIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const loadStats = useCallback(async () => {
    try {
      const [systemStatus, storageInfo] = await Promise.all([
        api.getSystemStatus(),
        api.getSystemStorage().catch(() => null),
      ]);

      const libraryBooks = systemStatus.library_book_count ?? systemStatus.library.book_count ?? 0;
      const importBooks =
        systemStatus.import_book_count ?? systemStatus.import_paths?.book_count ?? 0;
      const totalBooks = systemStatus.total_book_count ?? libraryBooks + importBooks;
      const totalFiles = systemStatus.total_file_count ?? totalBooks;
      const librarySizeBytes =
        systemStatus.library_size_bytes ?? systemStatus.library.total_size ?? 0;
      const importSizeBytes =
        systemStatus.import_size_bytes ?? systemStatus.import_paths?.total_size ?? 0;
      const totalSizeBytes = systemStatus.total_size_bytes ?? librarySizeBytes + importSizeBytes;

      // Prefer the dedicated storage endpoint which statfs's the actual data volume.
      // Fall back to system status fields only if the endpoint is unavailable.
      const diskTotalBytes = storageInfo?.total_bytes ?? systemStatus.disk_total_bytes ?? 0;
      const diskUsedBytes = storageInfo?.used_bytes ?? systemStatus.disk_used_bytes ?? 0;
      const diskUsagePercent =
        storageInfo?.percent_used ??
        (diskTotalBytes > 0 ? (diskUsedBytes / diskTotalBytes) * 100 : 0);

      setStats({
        library_books: libraryBooks,
        import_books: importBooks,
        total_books: totalBooks,
        total_files: totalFiles,
        total_authors: systemStatus.author_count ?? 0,
        total_series: systemStatus.series_count ?? 0,
        import_paths: systemStatus.import_paths?.folder_count || 0,
        library_size_gb: librarySizeBytes / (1024 * 1024 * 1024),
        import_size_gb: importSizeBytes / (1024 * 1024 * 1024),
        total_size_gb: totalSizeBytes / (1024 * 1024 * 1024),
        disk_used_gb: diskUsedBytes / (1024 * 1024 * 1024),
        disk_total_gb: diskTotalBytes / (1024 * 1024 * 1024),
        disk_usage_percent: diskUsagePercent,
      });
      setStatsError(null);

      // Broken files count (may be undefined)
      setBrokenFileCount((systemStatus as any).broken_file_count ?? null);

      // Convert recent operations. Two independent questions, two fields: what
      // to draw comes from `status`, whether to keep polling comes from
      // `completed_at`. Mapping a stopped op to 'running' would answer both
      // wrongly at once.
      const recentOps: RecentOperation[] = (systemStatus.operations?.recent || [])
        .slice(0, 5)
        .map((op) => {
          let status: RecentOperationStatus;
          switch (op.status) {
            case 'completed':
              status = 'success';
              break;
            case 'failed':
              status = 'error';
              break;
            // Stopped, not failed and not working: the user canceled it, or the
            // server went down under it. Drawing these as 'running' left a
            // canceled op indistinguishable from a live one.
            case 'canceled':
            case 'interrupted_dropped':
            case 'interrupted_quiesced':
            case 'interrupted_ask':
            case 'interrupted_restart':
              status = 'stopped';
              break;
            // 'running', 'queued', 'waiting_deps', and anything a future
            // registry version adds. An unknown status is far likelier to be a
            // new in-flight state than a new terminal one, and this only
            // decides an icon — `live` decides the polling.
            default:
              status = 'running';
          }
          return {
            id: op.id,
            type: op.type,
            status,
            live: !op.completed_at,
            message: op.message || `${op.type} operation`,
            timestamp: op.created_at,
          };
        });
      setOperations(recentOps);
    } catch (error) {
      console.error('Failed to load system status:', error);
      // Keep whatever stats and operations we last had. This used to write a
      // fully zeroed SystemStats and an empty operations list "so spinners
      // stop" — the spinners stop on the error state instead now, and a dead
      // backend no longer renders as an empty library with no history.
      setStatsError(describeError(error));
    }
  }, []);

  const loadAuthors = useCallback(async () => {
    try {
      const count = await api.countAuthors();
      setAuthorCount(count);
      setAuthorsError(null);
    } catch (error) {
      console.error('Failed to count authors:', error);
      setAuthorsError(describeError(error));
    }
  }, []);

  const loadSeries = useCallback(async () => {
    try {
      const count = await api.countSeries();
      setSeriesCount(count);
      setSeriesError(null);
    } catch (error) {
      console.error('Failed to count series:', error);
      setSeriesError(describeError(error));
    }
  }, []);

  const loadImportedCount = useCallback(async () => {
    try {
      const count = await api.countBooksFiltered({ libraryState: 'imported' });
      setImportedCount(count);
      setImportedError(null);
    } catch (error) {
      console.error('Failed to count imported books:', error);
      setImportedError(describeError(error));
    }
  }, []);

  // The loads that failed most recently, with the loader that retries each.
  // Only these re-fire on Retry — a count that loaded fine is not re-requested
  // because a sibling did not.
  const failedLoads = [
    { label: 'System status', message: statsError, retry: loadStats },
    { label: 'Author count', message: authorsError, retry: loadAuthors },
    { label: 'Series count', message: seriesError, retry: loadSeries },
    { label: 'Books awaiting organization', message: importedError, retry: loadImportedCount },
  ].filter((f): f is typeof f & { message: string } => f.message !== null);

  // Fire all requests in parallel — each section updates independently
  useEffect(() => {
    loadStats();
    loadAuthors();
    loadSeries();
    loadImportedCount();
  }, [loadStats, loadAuthors, loadSeries, loadImportedCount]);

  // Auto-refresh every 15s while a scan is active
  useEffect(() => {
    // `live`, not `status === 'running'`. This drives a 15s interval firing
    // four API calls, so a status that never resolves is a poll that never
    // stops — one per open dashboard tab, indefinitely.
    const hasActiveScan = operations?.some((op) => op.live);
    if (!hasActiveScan) {
      if (autoRefreshIntervalRef.current) {
        clearInterval(autoRefreshIntervalRef.current);
        autoRefreshIntervalRef.current = null;
      }
      return;
    }
    if (autoRefreshIntervalRef.current) clearInterval(autoRefreshIntervalRef.current);
    autoRefreshIntervalRef.current = setInterval(() => {
      loadStats();
      loadAuthors();
      loadSeries();
      loadImportedCount();
    }, 15000);
    return () => {
      if (autoRefreshIntervalRef.current) {
        clearInterval(autoRefreshIntervalRef.current);
        autoRefreshIntervalRef.current = null;
      }
    };
  }, [operations, loadStats, loadAuthors, loadSeries, loadImportedCount]);

  const handleScanAll = async () => {
    setScanInProgress(true);
    setActionNotice(null);
    try {
      const op = await api.startScan();
      useOperationsStore.getState().startPolling(op.id, 'scan');
      setActionNotice('Scan started for all import paths.');
      navigate('/operations');
    } catch (error) {
      console.error('Failed to start scan', error);
      setActionNotice('Failed to start scan.');
    } finally {
      setScanInProgress(false);
    }
  };

  const handleOrganizeAll = () => {
    setOrganizeDialogOpen(true);
  };

  const handleConfirmOrganizeAll = async () => {
    setOrganizeInProgress(true);
    setActionNotice(null);
    try {
      const op = await api.startOrganize(undefined, undefined, undefined, {
        syncITunesFirst,
      });
      useOperationsStore.getState().startPolling(op.id, 'organize');
      setActionNotice('Organize operation started.');
      setOrganizeDialogOpen(false);
      navigate('/operations');
    } catch (error) {
      console.error('Failed to start organize', error);
      setActionNotice('Failed to start organize.');
    } finally {
      setOrganizeInProgress(false);
    }
  };

  /**
   * One stat tile, in one of four DISTINGUISHABLE states:
   *   loading      — skeleton; the request is outstanding and nothing is known
   *   error, null  — "—" in error colour with "Count unavailable"; the request
   *                  failed and there is no last-known value to show
   *   error, value — the last-known value, with a warning that it may be stale
   *   value        — the number
   * `value` is null only until its request has succeeded once; a tile never
   * receives a fabricated 0 (WEB-03). The tile is a single button, so Retry
   * lives in the page-level banner rather than nested inside it.
   */
  const StatCard = ({
    title,
    value,
    loading,
    error,
    icon,
    suffix = '',
    subtitle,
    onClick,
    iconColor = 'primary.main',
    valueColor,
  }: {
    title: string;
    value: number | null;
    loading: boolean;
    error?: string | null;
    icon: React.ReactNode;
    suffix?: string;
    subtitle?: string;
    onClick?: () => void;
    iconColor?: string;
    valueColor?: string;
  }) => {
    const unavailable = !loading && value === null;
    const stale = !loading && value !== null && Boolean(error);
    return (
      <Tooltip title={error ? `Could not load ${title.toLowerCase()}: ${error}` : ''}>
        <Card>
          <CardActionArea onClick={onClick} disabled={!onClick}>
            <CardContent>
              <Box
                sx={{
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                }}
              >
                <Box>
                  <Typography
                    gutterBottom
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    {title}
                  </Typography>
                  {loading ? (
                    <Skeleton variant="text" width={80} height={42} />
                  ) : value === null ? (
                    <>
                      <Typography
                        variant="h4"
                        color="error.main"
                        data-testid={`stat-unavailable-${title}`}
                      >
                        —
                      </Typography>
                      <Typography variant="caption" sx={{ color: 'error.main' }}>
                        Count unavailable
                      </Typography>
                    </>
                  ) : (
                    <>
                      <Typography variant="h4" color={valueColor}>
                        {value.toLocaleString()}
                        {suffix}
                      </Typography>
                      {subtitle && (
                        <Typography
                          variant="caption"
                          sx={{
                            color: 'text.secondary',
                          }}
                        >
                          {subtitle}
                        </Typography>
                      )}
                      {stale && (
                        <Typography
                          variant="caption"
                          data-testid={`stat-stale-${title}`}
                          sx={{ color: 'warning.main', display: 'block' }}
                        >
                          Could not refresh — may be out of date
                        </Typography>
                      )}
                    </>
                  )}
                </Box>
                <Box
                  sx={{
                    color: unavailable && error ? 'error.main' : stale ? 'warning.main' : iconColor,
                  }}
                >
                  {icon}
                </Box>
              </Box>
            </CardContent>
          </CardActionArea>
        </Card>
      </Tooltip>
    );
  };

  // Both helpers take RecentOperationStatus, not string, so adding a state to
  // the union makes tsc point at every switch that has not handled it. They
  // used to take `string` and be called through an `as` cast at the call site,
  // which type-checked whatever was passed and would have silently swallowed
  // the 'stopped' state added above.
  const getStatusIcon = (status: RecentOperationStatus) => {
    switch (status) {
      case 'success':
        return <CheckCircleIcon color="success" />;
      case 'error':
        return <ErrorIcon color="error" />;
      case 'stopped':
        return <PauseCircleOutlineIcon color="disabled" />;
      default:
        return <CheckCircleIcon color="action" />;
    }
  };

  const getStatusColor = (status: RecentOperationStatus): ChipColor => {
    switch (status) {
      case 'success':
        return 'success';
      case 'error':
        return 'error';
      case 'stopped':
        return 'warning';
      default:
        return 'default';
    }
  };

  // Derive loading states per component. "Loading" means the request is still
  // outstanding — a request that FAILED is not loading, it is in error, and the
  // tile renders that instead of a skeleton that never resolves.
  const bookStatsLoading = stats === null && !statsError;
  // The author/series tiles fall back to the system-status totals while their
  // own count request is outstanding or has failed. loadStats stores a missing
  // author_count/series_count as 0, so a 0 there is "not delivered", not a
  // count — `||` treats it as no fallback, exactly as the old
  // `!stats.total_authors` loading check did. The fallback is null (not 0)
  // when there is nothing to fall back to.
  const authorsValue = authorCount ?? (stats?.total_authors || null);
  const seriesValue = seriesCount ?? (stats?.total_series || null);
  const authorsLoading = authorsValue === null && !authorsError && !statsError;
  const seriesLoading = seriesValue === null && !seriesError && !statsError;
  const importedLoading = importedCount === null && !importedError;

  return (
    <Box sx={{ height: '100%', overflow: 'auto' }}>
      <Typography variant="h4" gutterBottom>
        Dashboard
      </Typography>

      <AnnouncementBanner />

      {actionNotice && (
        <Alert severity="info" sx={{ mb: 2 }}>
          {actionNotice}
        </Alert>
      )}

      {/* Page-level report of every load that failed, with one Retry that
          re-fires only those. The tiles carry the per-widget marker; this is
          where the message and the retry affordance live (a tile is itself a
          button, so it cannot host one). */}
      {failedLoads.length > 0 && (
        <Alert
          severity="error"
          data-testid="dashboard-load-error"
          sx={{ mb: 2 }}
          action={
            <Button
              color="inherit"
              size="small"
              onClick={() => {
                for (const f of failedLoads) void f.retry();
              }}
            >
              Retry
            </Button>
          }
        >
          <AlertTitle>Some dashboard data could not be loaded</AlertTitle>
          {failedLoads.map((f) => `${f.label}: ${f.message}`).join(' · ')}
        </Alert>
      )}

      <Grid container spacing={3}>
        <Grid
          size={{
            xs: 12,
            sm: 6,
            md: 3,
          }}
        >
          <StatCard
            title="Library Books"
            value={stats?.library_books ?? null}
            loading={bookStatsLoading}
            error={statsError}
            icon={<LibraryBooksIcon sx={{ fontSize: 40 }} />}
            subtitle={
              stats && stats.total_files > stats.total_books
                ? `${stats.total_files.toLocaleString()} files total`
                : undefined
            }
            onClick={() => navigate('/library')}
          />
        </Grid>

        <Grid
          size={{
            xs: 12,
            sm: 6,
            md: 3,
          }}
        >
          <StatCard
            title="Import Path Books"
            value={stats?.import_books ?? null}
            loading={bookStatsLoading}
            error={statsError}
            icon={<FolderIcon sx={{ fontSize: 40 }} />}
            onClick={() => navigate('/library?state=imported')}
          />
        </Grid>

        <Grid
          size={{
            xs: 12,
            sm: 6,
            md: 3,
          }}
        >
          <StatCard
            title="Authors"
            value={authorsValue}
            loading={authorsLoading}
            error={authorsError}
            icon={<PersonIcon sx={{ fontSize: 40 }} />}
            onClick={() => navigate('/authors')}
          />
        </Grid>

        <Grid
          size={{
            xs: 12,
            sm: 6,
            md: 3,
          }}
        >
          <StatCard
            title="Series"
            value={seriesValue}
            loading={seriesLoading}
            error={seriesError}
            icon={<MenuBookIcon sx={{ fontSize: 40 }} />}
            onClick={() => navigate('/series')}
          />
        </Grid>

        <Grid
          size={{
            xs: 12,
            sm: 6,
            md: 3,
          }}
        >
          <StatCard
            title="Broken Files"
            value={brokenFileCount ?? 0}
            loading={brokenFileCount === null}
            icon={<WarningIcon sx={{ fontSize: 40 }} />}
            subtitle={brokenFileCount !== null ? 'books with broken files' : undefined}
            onClick={() => navigate('/library?has_file_errors=true')}
            iconColor={'warning.main'}
            valueColor={
              brokenFileCount !== null && brokenFileCount > 0 ? 'warning.main' : undefined
            }
          />
        </Grid>

        <Grid
          size={{
            xs: 12,
            sm: 6,
            md: 3,
          }}
        >
          {(() => {
            const allOrganized = !importedLoading && importedCount === 0;
            return (
              <StatCard
                title={allOrganized ? 'All Books Organized' : 'Needs Organizing'}
                value={importedCount}
                loading={importedLoading}
                error={importedError}
                icon={
                  allOrganized ? (
                    <CheckCircleIcon sx={{ fontSize: 40 }} />
                  ) : (
                    <WarningIcon sx={{ fontSize: 40 }} />
                  )
                }
                subtitle={
                  allOrganized
                    ? 'Library is fully organized'
                    : importedCount !== null && importedCount > 0
                      ? 'books awaiting organization'
                      : undefined
                }
                iconColor={
                  importedLoading ? 'primary.main' : allOrganized ? 'success.main' : 'warning.main'
                }
                valueColor={
                  importedLoading ? undefined : allOrganized ? 'success.main' : 'warning.main'
                }
                onClick={() => navigate('/library?state=imported')}
              />
            );
          })()}
        </Grid>

        <Grid
          size={{
            xs: 12,
            md: 6,
          }}
        >
          <Paper sx={{ p: 3 }}>
            <Typography variant="h6" gutterBottom>
              Storage Usage
            </Typography>
            {/* stats is null until system status has loaded once. With an
                error and no stats there is nothing to draw — say so rather
                than draw "0.0 GB / 0.0 GB" (which is what the zeroed fallback
                used to render). */}
            {stats === null && statsError ? (
              <Alert severity="error" data-testid="storage-error">
                Storage usage unavailable: {statsError}
              </Alert>
            ) : stats === null ? (
              <Box sx={{ py: 2 }}>
                <Skeleton variant="text" width="60%" height={24} />
                <Skeleton variant="rectangular" height={8} sx={{ my: 1, borderRadius: 1 }} />
                <Skeleton variant="text" width="40%" height={20} />
              </Box>
            ) : (
              <>
                <Box sx={{ mb: 2 }}>
                  <Box
                    sx={{
                      display: 'flex',
                      justifyContent: 'space-between',
                      mb: 1,
                    }}
                  >
                    <Typography
                      variant="body2"
                      sx={{
                        color: 'text.secondary',
                      }}
                    >
                      Total Size
                    </Typography>
                    <Typography
                      variant="body2"
                      sx={{
                        fontWeight: 'medium',
                      }}
                    >
                      {(stats?.disk_used_gb ?? 0).toFixed(1)} GB /{' '}
                      {(stats?.disk_total_gb ?? 0).toFixed(1)} GB
                    </Typography>
                  </Box>
                  <LinearProgress
                    variant="determinate"
                    value={stats?.disk_usage_percent ?? 0}
                    sx={{ height: 8, borderRadius: 1 }}
                  />
                  <Typography
                    variant="caption"
                    sx={{
                      color: 'text.secondary',
                      mt: 0.5,
                      display: 'block',
                    }}
                  >
                    {(stats?.disk_usage_percent ?? 0).toFixed(0)}% of disk used
                  </Typography>
                </Box>
                <Box
                  sx={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 1,
                  }}
                >
                  <StorageIcon color="action" />
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    System storage healthy
                  </Typography>
                </Box>
              </>
            )}
          </Paper>
        </Grid>

        <Grid
          size={{
            xs: 12,
            md: 6,
          }}
        >
          <Paper sx={{ p: 3 }}>
            <Typography variant="h6" gutterBottom>
              Recent Operations
            </Typography>
            {/* Same four states as the tiles. operations is null until system
                status has loaded once; a failed load keeps the previous list,
                and with none to keep it renders the error, not "No recent
                operations". */}
            {operations === null && statsError ? (
              <Alert severity="error" data-testid="recent-operations-error">
                Recent operations unavailable: {statsError}
              </Alert>
            ) : operations === null ? (
              <Box sx={{ py: 1 }}>
                <Skeleton variant="text" width="80%" />
                <Skeleton variant="text" width="60%" />
                <Skeleton variant="text" width="70%" />
              </Box>
            ) : operations.length === 0 ? (
              <Typography
                variant="body2"
                sx={{
                  color: 'text.secondary',
                }}
              >
                No recent operations
              </Typography>
            ) : (
              <List dense>
                {operations.map((op) => (
                  <ListItem key={op.id} sx={{ px: 0 }}>
                    <Box
                      sx={{
                        display: 'flex',
                        alignItems: 'center',
                        width: '100%',
                      }}
                    >
                      {getStatusIcon(op.status)}
                      <ListItemText
                        primary={op.message}
                        secondary={new Date(op.timestamp).toLocaleTimeString()}
                        sx={{ ml: 1 }}
                      />
                      <Chip
                        label={op.type}
                        size="small"
                        color={getStatusColor(op.status)}
                        variant="outlined"
                      />
                    </Box>
                  </ListItem>
                ))}
              </List>
            )}
          </Paper>
        </Grid>

        <Grid size={12}>
          <Paper sx={{ p: 3 }}>
            <Typography variant="h6" gutterBottom>
              Quick Actions
            </Typography>
            <Stack direction={{ xs: 'column', sm: 'row' }} spacing={2}>
              <Button
                variant="contained"
                onClick={handleScanAll}
                disabled={scanInProgress}
                startIcon={scanInProgress ? <CircularProgress size={20} /> : undefined}
              >
                {scanInProgress ? 'Starting Scan...' : 'Scan All Import Paths'}
              </Button>
              <Button variant="outlined" onClick={handleOrganizeAll}>
                Organize All
              </Button>
            </Stack>
          </Paper>
        </Grid>
      </Grid>

      <Dialog open={organizeDialogOpen} onClose={() => setOrganizeDialogOpen(false)}>
        <DialogTitle>Organize All Scanned Books</DialogTitle>
        <DialogContent>
          <Typography
            variant="body2"
            sx={{
              color: 'text.secondary',
              mb: 2,
            }}
          >
            This will organize all books currently scanned but not yet imported to the library.
          </Typography>
          <FormControlLabel
            control={
              <Checkbox
                checked={syncITunesFirst}
                onChange={(e) => setSyncITunesFirst(e.target.checked)}
              />
            }
            label="Sync iTunes library first"
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setOrganizeDialogOpen(false)} disabled={organizeInProgress}>
            Cancel
          </Button>
          <Button
            variant="contained"
            onClick={handleConfirmOrganizeAll}
            disabled={organizeInProgress}
            startIcon={organizeInProgress ? <CircularProgress size={20} /> : undefined}
          >
            {organizeInProgress ? 'Organizing...' : 'Organize'}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
