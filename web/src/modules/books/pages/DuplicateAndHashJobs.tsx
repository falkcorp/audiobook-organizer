// file: web/src/modules/books/pages/DuplicateAndHashJobs.tsx
// version: 0.1.0
// guid: 0f62ee3f-baed-494c-933e-a09be65c9553
// last-edited: 2026-11-21

// Books half of the Maintenance tab: the SHA duplicate, AcoustID coverage and metadata-hash jobs, keeping their legacy api calls (F9-04 slice 1).

import { useEffect, useState, useCallback } from 'react';
import {
  Alert,
  Box,
  Button,
  Card,
  CardActions,
  CardContent,
  CardHeader,
  Chip,
  CircularProgress,
  Collapse,
  List,
  ListItem,
  ListItemText,
  Stack,
  Typography,
} from '@mui/material';
import * as api from '../../../services/api';

function SHADuplicateCard() {
  const [scanning, setScanning] = useState(false);
  const [result, setResult] = useState<api.DuplicateFilesResult | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Hash coverage stats
  const [stats, setStats] = useState<api.BookFileHashStats | null>(null);
  const [statsLoading, setStatsLoading] = useState(false);
  const [backfilling, setBackfilling] = useState(false);
  const [backfillOpID, setBackfillOpID] = useState<string | null>(null);

  const loadStats = useCallback(async () => {
    setStatsLoading(true);
    try {
      setStats(await api.getBookFileHashStats());
    } catch {
      // non-fatal — stats may not be available yet
    } finally {
      setStatsLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadStats();
  }, [loadStats]);

  const handleScan = useCallback(async () => {
    setScanning(true);
    setError(null);
    setResult(null);
    try {
      const r = await api.scanDuplicateFiles(50);
      setResult(r);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Scan failed');
    } finally {
      setScanning(false);
    }
  }, []);

  const handleBackfill = useCallback(async () => {
    setBackfilling(true);
    setBackfillOpID(null);
    setError(null);
    try {
      const r = await api.backfillFileHashes(false);
      setBackfillOpID(r.operation_id);
      await loadStats();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Backfill failed');
    } finally {
      setBackfilling(false);
    }
  }, [loadStats]);

  const fmt = (bytes: number) => {
    if (bytes >= 1_073_741_824) return `${(bytes / 1_073_741_824).toFixed(1)} GB`;
    if (bytes >= 1_048_576) return `${(bytes / 1_048_576).toFixed(1)} MB`;
    return `${(bytes / 1024).toFixed(0)} KB`;
  };

  const pct = (n: number, d: number) => (d === 0 ? '—' : `${((n / d) * 100).toFixed(1)}%`);

  const statusChip = (() => {
    if (statsLoading) return <Chip size="small" label="Loading…" />;
    if (!stats) return null;
    if (stats.missing_file_hash === 0)
      return <Chip size="small" color="success" label="✓ All hashed" />;
    return (
      <Chip size="small" color="warning" label={`${stats.missing_file_hash} missing hashes`} />
    );
  })();

  return (
    <Card variant="outlined" sx={{ mb: 2 }}>
      <CardHeader
        title="SHA Duplicate Detection"
        subheader="Files with identical SHA-256 content. Also tracks hash coverage for multi-file dedup."
        action={statusChip}
      />
      <CardContent>
        {error && (
          <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>
            {error}
          </Alert>
        )}

        {/* Hash coverage stats */}
        {stats && (
          <Box sx={{ mb: 2, p: 1.5, bgcolor: 'action.hover', borderRadius: 1 }}>
            <Typography variant="subtitle2" gutterBottom>
              Hash Coverage
            </Typography>
            <Stack
              direction="row"
              spacing={3}
              useFlexGap
              sx={{
                flexWrap: 'wrap',
              }}
            >
              <Box>
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  Total files
                </Typography>
                <Typography
                  variant="body2"
                  sx={{
                    fontWeight: 'bold',
                  }}
                >
                  {stats.total_book_files.toLocaleString()}
                </Typography>
              </Box>
              <Box>
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  With hash
                </Typography>
                <Typography
                  variant="body2"
                  sx={{
                    fontWeight: 'bold',
                  }}
                >
                  {stats.with_file_hash.toLocaleString()} (
                  {pct(stats.with_file_hash, stats.total_book_files)})
                </Typography>
              </Box>
              <Box>
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  Missing hash
                </Typography>
                <Typography
                  variant="body2"
                  color={stats.missing_file_hash > 0 ? 'warning.main' : 'success.main'}
                  sx={{
                    fontWeight: 'bold',
                  }}
                >
                  {stats.missing_file_hash.toLocaleString()}
                </Typography>
              </Box>
              <Box>
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  With orig hash
                </Typography>
                <Typography
                  variant="body2"
                  sx={{
                    fontWeight: 'bold',
                  }}
                >
                  {stats.with_original_hash.toLocaleString()}
                </Typography>
              </Box>
              <Box>
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  Books (no files)
                </Typography>
                <Typography
                  variant="body2"
                  color={stats.books_with_no_files > 0 ? 'warning.main' : 'text.primary'}
                  sx={{
                    fontWeight: 'bold',
                  }}
                >
                  {stats.books_with_no_files.toLocaleString()} /{' '}
                  {stats.total_books.toLocaleString()}
                </Typography>
              </Box>
            </Stack>

            {(stats.by_library?.length ?? 0) > 1 && (
              <Box sx={{ mt: 1.5 }}>
                <Typography
                  variant="caption"
                  gutterBottom
                  sx={{
                    color: 'text.secondary',
                    display: 'block',
                  }}
                >
                  By Library
                </Typography>
                {stats.by_library.map((lib) => (
                  <Stack
                    key={lib.path}
                    direction="row"
                    spacing={1}
                    sx={{
                      alignItems: 'center',
                      mb: 0.5,
                    }}
                  >
                    <Typography
                      variant="caption"
                      sx={{
                        minWidth: 200,
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                      }}
                      title={lib.path}
                    >
                      {lib.path.split('/').pop() || lib.path}
                    </Typography>
                    <Typography
                      variant="caption"
                      sx={{
                        color: 'text.secondary',
                      }}
                    >
                      {lib.with_hash}/{lib.total_files} hashed
                    </Typography>
                    {lib.missing_hash > 0 && (
                      <Chip
                        size="small"
                        label={`${lib.missing_hash} missing`}
                        color="warning"
                        variant="outlined"
                      />
                    )}
                  </Stack>
                ))}
              </Box>
            )}
          </Box>
        )}

        <Stack
          direction="row"
          spacing={2}
          useFlexGap
          sx={{
            flexWrap: 'wrap',
            mb: 2,
          }}
        >
          <Button
            variant="outlined"
            startIcon={scanning ? <CircularProgress size={14} /> : undefined}
            disabled={scanning}
            onClick={handleScan}
          >
            {scanning ? 'Scanning…' : 'Scan for SHA Duplicates'}
          </Button>
          <Button
            variant="outlined"
            color="secondary"
            startIcon={backfilling ? <CircularProgress size={14} /> : undefined}
            disabled={backfilling || statsLoading}
            onClick={handleBackfill}
          >
            {backfilling ? 'Backfilling…' : 'Backfill Missing Hashes'}
          </Button>
          <Button
            size="small"
            variant="text"
            disabled={statsLoading}
            onClick={() => void loadStats()}
          >
            Refresh Stats
          </Button>
        </Stack>

        {backfillOpID && (
          <Alert severity="info" sx={{ mb: 2 }} onClose={() => setBackfillOpID(null)}>
            Backfill job started — operation ID: <strong>{backfillOpID}</strong>
          </Alert>
        )}

        {result && (
          <Typography variant="body2" sx={{ mb: 1 }}>
            Found <strong>{result.total_groups}</strong> duplicate group(s) —{' '}
            <strong>{fmt(result.total_wasted_bytes)}</strong> wasted space.
          </Typography>
        )}

        {result && (result.groups?.length ?? 0) > 0 && (
          <List dense disablePadding>
            {(result.groups ?? []).map((g) => (
              <Box key={g.hash} sx={{ mb: 1 }}>
                <ListItem
                  disableGutters
                  sx={{ cursor: 'pointer' }}
                  onClick={() => setExpanded(expanded === g.hash ? null : g.hash)}
                >
                  <ListItemText
                    primary={`${g.files[0]?.book_title || '(unknown)'} — ${g.file_count} copies`}
                    secondary={`${fmt(g.total_size_bytes)} total · hash: ${g.hash.slice(0, 12)}…`}
                  />
                  <Chip size="small" label={`${g.file_count} files`} sx={{ ml: 1 }} />
                </ListItem>
                <Collapse in={expanded === g.hash}>
                  <List dense disablePadding sx={{ pl: 2 }}>
                    {g.files.map((f) => (
                      <ListItem key={f.book_file_id} disableGutters>
                        <ListItemText
                          primary={f.file_path || f.book_path}
                          secondary={f.book_title}
                        />
                        <Chip
                          size="small"
                          label={fmt(f.file_size_bytes)}
                          variant="outlined"
                          sx={{ ml: 1 }}
                        />
                      </ListItem>
                    ))}
                  </List>
                </Collapse>
              </Box>
            ))}
          </List>
        )}
      </CardContent>
    </Card>
  );
}

function AcoustIDFingerprintCard() {
  const [stats, setStats] = useState<api.AcoustIDStats | null>(null);
  const [loading, setLoading] = useState(false);
  const [triggering, setTriggering] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadStats = useCallback(async () => {
    setLoading(true);
    try {
      setStats(await api.getAcoustIDStats());
    } catch {
      // degrade silently
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadStats();
  }, [loadStats]);

  const handleFingerprint = useCallback(async () => {
    setTriggering(true);
    setError(null);
    try {
      await api.runMaintenanceJob('backfill-file-hashes', false);
      await loadStats();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to trigger fingerprint job');
    } finally {
      setTriggering(false);
    }
  }, [loadStats]);

  const pct =
    stats && stats.total_files > 0
      ? Math.round((stats.with_fingerprint / stats.total_files) * 100)
      : 0;

  return (
    <Card sx={{ mb: 2 }}>
      <CardHeader
        title="AcoustID Fingerprint Coverage"
        subheader="Files with at least one AcoustID fingerprint segment populated."
        action={loading ? <CircularProgress size={20} sx={{ mt: 1, mr: 1 }} /> : null}
      />
      <CardContent>
        {stats ? (
          <>
            <Typography variant="h4" gutterBottom>
              {pct}%
            </Typography>
            <Typography
              variant="body2"
              gutterBottom
              sx={{
                color: 'text.secondary',
              }}
            >
              {stats.with_fingerprint.toLocaleString()} / {stats.total_files.toLocaleString()} files
              fingerprinted
            </Typography>
            <Chip
              label={pct === 100 ? '✓ Complete' : 'Partial coverage'}
              color={pct === 100 ? 'success' : 'warning'}
              size="small"
            />
          </>
        ) : (
          !loading && (
            <Typography
              variant="body2"
              sx={{
                color: 'text.secondary',
              }}
            >
              No data available
            </Typography>
          )
        )}
        {error && (
          <Alert severity="error" sx={{ mt: 1 }} onClose={() => setError(null)}>
            {error}
          </Alert>
        )}
      </CardContent>
      <CardActions>
        <Button
          onClick={handleFingerprint}
          disabled={triggering}
          variant="outlined"
          size="small"
          startIcon={triggering ? <CircularProgress size={14} /> : undefined}
        >
          {triggering ? 'Starting…' : 'Fingerprint Library'}
        </Button>
        <Button onClick={loadStats} size="small" disabled={loading}>
          Refresh
        </Button>
      </CardActions>
    </Card>
  );
}

function MetadataHashDuplicateCard() {
  const [scanning, setScanning] = useState(false);
  const [result, setResult] = useState<api.MetadataHashDuplicatesResult | null>(null);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // NEW: stats + backfill
  const [stats, setStats] = useState<api.BookMetadataHashStats | null>(null);
  const [statsLoading, setStatsLoading] = useState(false);
  const [backfilling, setBackfilling] = useState(false);
  const [backfillOpID, setBackfillOpID] = useState<string | null>(null);

  const loadStats = useCallback(async () => {
    setStatsLoading(true);
    try {
      setStats(await api.getBookMetadataHashStats());
    } catch {
      // degrade silently if endpoint not available yet
    } finally {
      setStatsLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadStats();
  }, [loadStats]);

  const handleScan = useCallback(async () => {
    setScanning(true);
    setError(null);
    setResult(null);
    try {
      const r = await api.findMetadataHashDuplicates();
      setResult(r);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Scan failed');
    } finally {
      setScanning(false);
    }
  }, []);

  const handleBackfill = useCallback(async () => {
    setBackfilling(true);
    setError(null);
    setBackfillOpID(null);
    try {
      const r = await api.backfillMetadataHashes(false);
      // returns { operation_id }
      setBackfillOpID(r.operation_id ?? null);
      await loadStats();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Backfill failed');
    } finally {
      setBackfilling(false);
    }
  }, [loadStats]);

  const pct = (n: number, d: number) => (d === 0 ? '—' : `${((n / d) * 100).toFixed(1)}%`);

  const statusChip = (() => {
    if (statsLoading) return <Chip size="small" label="Loading…" />;
    if (!stats) return null;
    if (stats.missing_metadata_hash === 0)
      return <Chip size="small" color="success" label="✓ All hashed" />;
    return (
      <Chip size="small" color="warning" label={`${stats.missing_metadata_hash} missing hashes`} />
    );
  })();

  return (
    <Card variant="outlined" sx={{ mb: 2 }}>
      <CardHeader
        title="Metadata Hash Duplicates"
        subheader="Books that share the same metadata source hash (same ASIN/ISBN from the same provider)."
        action={statusChip}
      />
      <CardContent>
        {error && (
          <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>
            {error}
          </Alert>
        )}

        {/* Stats panel */}
        {stats && (
          <Box sx={{ mb: 2, p: 1.5, bgcolor: 'action.hover', borderRadius: 1 }}>
            <Typography variant="subtitle2" gutterBottom>
              Metadata Hash Coverage
            </Typography>
            <Stack
              direction="row"
              spacing={3}
              useFlexGap
              sx={{
                flexWrap: 'wrap',
              }}
            >
              <Box>
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  Total books
                </Typography>
                <Typography
                  variant="body2"
                  sx={{
                    fontWeight: 'bold',
                  }}
                >
                  {stats.total_books.toLocaleString()}
                </Typography>
              </Box>
              <Box>
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  With metadata hash
                </Typography>
                <Typography
                  variant="body2"
                  sx={{
                    fontWeight: 'bold',
                  }}
                >
                  {stats.with_metadata_hash.toLocaleString()} (
                  {pct(stats.with_metadata_hash, stats.total_books)})
                </Typography>
              </Box>
              <Box>
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  Missing metadata hash
                </Typography>
                <Typography
                  variant="body2"
                  color={stats.missing_metadata_hash > 0 ? 'warning.main' : 'success.main'}
                  sx={{
                    fontWeight: 'bold',
                  }}
                >
                  {stats.missing_metadata_hash.toLocaleString()}
                </Typography>
              </Box>
              <Box>
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  Has ASIN/ISBN
                </Typography>
                <Typography
                  variant="body2"
                  sx={{
                    fontWeight: 'bold',
                  }}
                >
                  {stats.with_asin_or_isbn.toLocaleString()}
                </Typography>
              </Box>
              <Box>
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  Missing hash but has ID
                </Typography>
                <Typography
                  variant="body2"
                  sx={{
                    fontWeight: 'bold',
                  }}
                >
                  {stats.missing_hash_has_id.toLocaleString()}
                </Typography>
              </Box>
            </Stack>

            {(stats.by_library?.length ?? 0) > 1 && (
              <Box sx={{ mt: 1.5 }}>
                <Typography
                  variant="caption"
                  gutterBottom
                  sx={{
                    color: 'text.secondary',
                    display: 'block',
                  }}
                >
                  By Library
                </Typography>
                {stats.by_library.map((lib) => (
                  <Stack
                    key={lib.path}
                    direction="row"
                    spacing={1}
                    sx={{
                      alignItems: 'center',
                      mb: 0.5,
                    }}
                  >
                    <Typography
                      variant="caption"
                      sx={{
                        minWidth: 200,
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                      }}
                      title={lib.path}
                    >
                      {lib.path.split('/').pop() || lib.path}
                    </Typography>
                    <Typography
                      variant="caption"
                      sx={{
                        color: 'text.secondary',
                      }}
                    >
                      {lib.with_metadata_hash}/{lib.total_books} hashed
                    </Typography>
                    {lib.missing_metadata_hash > 0 && (
                      <Chip
                        size="small"
                        label={`${lib.missing_metadata_hash} missing`}
                        color="warning"
                        variant="outlined"
                      />
                    )}
                  </Stack>
                ))}
              </Box>
            )}
          </Box>
        )}

        <Stack direction="row" spacing={2} sx={{ mb: 2 }}>
          <Button
            variant="outlined"
            startIcon={scanning ? <CircularProgress size={14} /> : undefined}
            disabled={scanning}
            onClick={handleScan}
          >
            {scanning ? 'Scanning…' : 'Scan for Metadata Hash Duplicates'}
          </Button>

          <Button
            variant="outlined"
            color="secondary"
            startIcon={backfilling ? <CircularProgress size={14} /> : undefined}
            disabled={backfilling || statsLoading}
            onClick={handleBackfill}
          >
            {backfilling ? 'Starting…' : 'Backfill Missing Hashes'}
          </Button>

          <Button
            size="small"
            variant="text"
            disabled={statsLoading}
            onClick={() => void loadStats()}
          >
            Refresh Stats
          </Button>
        </Stack>

        {backfillOpID && (
          <Alert severity="info" sx={{ mb: 2 }} onClose={() => setBackfillOpID(null)}>
            Backfill job started — operation ID: <strong>{backfillOpID}</strong>
          </Alert>
        )}

        {result && result.groups.length > 0 && (
          <List dense disablePadding>
            {result.groups.map((g) => (
              <Box key={g.hash} sx={{ mb: 1 }}>
                <ListItem
                  disableGutters
                  sx={{ cursor: 'pointer' }}
                  onClick={() => setExpanded(expanded === g.hash ? null : g.hash)}
                >
                  <ListItemText
                    primary={`${g.books[0]?.title || '(unknown)'} — ${g.books.length} copies`}
                    secondary={`hash: ${g.hash.slice(0, 16)}…`}
                  />
                  <Chip size="small" label={`${g.books.length} books`} sx={{ ml: 1 }} />
                </ListItem>
                <Collapse in={expanded === g.hash}>
                  <List dense disablePadding sx={{ pl: 2 }}>
                    {g.books.map((b) => (
                      <ListItem key={b.id} disableGutters>
                        <ListItemText primary={b.title} secondary={`ID: ${b.id}`} />
                        <Chip
                          size="small"
                          label={`${b.file_count} files`}
                          variant="outlined"
                          sx={{ ml: 1 }}
                        />
                      </ListItem>
                    ))}
                  </List>
                </Collapse>
              </Box>
            ))}
          </List>
        )}
      </CardContent>
    </Card>
  );
}

/** The hash, AcoustID and duplicate jobs of the old Maintenance tab: one books widget (07 §7.11 FE8-6). */
export function DuplicateAndHashJobs() {
  return (
    <>
      <SHADuplicateCard />

      <AcoustIDFingerprintCard />

      <MetadataHashDuplicateCard />
    </>
  );
}
