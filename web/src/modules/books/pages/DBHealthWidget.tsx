// file: web/src/modules/books/pages/DBHealthWidget.tsx
// version: 0.1.0
// guid: d0cb330c-b9f6-488c-9995-fb7ca1fc0eef
// last-edited: 2026-11-21

// Books diagnostics widget: the database health card of the old Diagnostics page (F9-04 slice 3).

import { useState, useCallback } from 'react';
import {
  Alert,
  Accordion,
  AccordionSummary,
  AccordionDetails,
  Box,
  Button,
  CircularProgress,
  Grid,
  Paper,
  Stack,
  Table,
  TableContainer,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import * as api from '../../../services/api';

export function DBHealthWidget() {
  // DB health stats
  const [dbHealth, setDbHealth] = useState<api.DBHealthStats | null>(null);
  const [dbHealthLoading, setDbHealthLoading] = useState(false);
  const [dbHealthError, setDbHealthError] = useState<string | null>(null);

  const [countingExpired, setCountingExpired] = useState(false);
  const [expiredError, setExpiredError] = useState<string | null>(null);

  // Count expired metadata-cache rows on demand: ?deep=true decodes every row,
  // so it is never part of the default load. Only the expired fields are
  // merged, so the other figures on the card do not shift under the user.
  const countExpired = useCallback(async () => {
    setCountingExpired(true);
    setExpiredError(null);
    try {
      const deep = await api.getDBHealthStats(true);
      if (deep.metadata_cache.expired_entries_computed !== true) {
        setExpiredError(
          `Expired count failed: ${
            deep.metadata_cache.expired_entries_error ?? 'the server did not compute the count'
          }`
        );
      }
      setDbHealth((prev) =>
        prev
          ? {
              ...prev,
              metadata_cache: {
                ...prev.metadata_cache,
                expired_entries: deep.metadata_cache.expired_entries,
                expired_entries_computed: deep.metadata_cache.expired_entries_computed,
                expired_entries_elapsed_ms: deep.metadata_cache.expired_entries_elapsed_ms,
                expired_entries_pages_walked: deep.metadata_cache.expired_entries_pages_walked,
              },
            }
          : deep
      );
    } catch (e) {
      setExpiredError(
        `Expired count failed: ${e instanceof Error ? e.message : 'the db-health?deep=true request did not complete'}`
      );
    } finally {
      setCountingExpired(false);
    }
  }, []);

  const fetchDBHealth = useCallback(async () => {
    setDbHealthLoading(true);
    setDbHealthError(null);
    // A refresh drops the expired fields, so a caption from an earlier failed
    // count no longer describes anything on the card.
    setExpiredError(null);
    try {
      const stats = await api.getDBHealthStats();
      setDbHealth(stats);
    } catch (e) {
      setDbHealthError(e instanceof Error ? e.message : 'Failed to load DB health');
    } finally {
      setDbHealthLoading(false);
    }
  }, []);

  return (
    <Accordion>
        <AccordionSummary
          expandIcon={<ExpandMoreIcon />}
          onClick={() => {
            if (!dbHealth && !dbHealthLoading) fetchDBHealth();
          }}
        >
          <Typography
            variant="h6"
            sx={{
              fontWeight: 'bold',
            }}
          >
            Database Health
          </Typography>
        </AccordionSummary>
        <AccordionDetails>
          <Stack spacing={2}>
            <Box sx={{ display: 'flex', justifyContent: 'flex-end' }}>
              <Button
                size="small"
                variant="outlined"
                onClick={fetchDBHealth}
                disabled={dbHealthLoading}
              >
                {dbHealthLoading ? <CircularProgress size={16} sx={{ mr: 1 }} /> : null}
                Refresh
              </Button>
            </Box>

            {dbHealthError && <Alert severity="error">{dbHealthError}</Alert>}

            {dbHealth && (
              <Stack spacing={2}>
                {/* SQLite main store tables */}
                {dbHealth.sqlite && (
                  <Paper variant="outlined" sx={{ p: 2 }}>
                    <Typography
                      variant="subtitle2"
                      gutterBottom
                      sx={{
                        fontWeight: 'bold',
                      }}
                    >
                      SQLite — Main Store ({(dbHealth.sqlite.size_bytes / 1024 / 1024).toFixed(1)}{' '}
                      MB)
                    </Typography>
                    <TableContainer>
                      <Table size="small">
                        <TableHead>
                          <TableRow>
                            <TableCell>Table</TableCell>
                            <TableCell align="right">Row Count</TableCell>
                          </TableRow>
                        </TableHead>
                        <TableBody>
                          {dbHealth.sqlite.tables.map((t) => (
                            <TableRow key={t.name}>
                              <TableCell>{t.name}</TableCell>
                              <TableCell align="right">{t.row_count.toLocaleString()}</TableCell>
                            </TableRow>
                          ))}
                        </TableBody>
                      </Table>
                    </TableContainer>
                  </Paper>
                )}

                {/* PebbleDB main store */}
                {dbHealth.pebble && (
                  <Paper variant="outlined" sx={{ p: 2 }}>
                    <Typography
                      variant="subtitle2"
                      gutterBottom
                      sx={{
                        fontWeight: 'bold',
                      }}
                    >
                      PebbleDB — Main Store
                    </Typography>
                    <Stack direction="row" spacing={4}>
                      <Box>
                        <Typography
                          variant="caption"
                          sx={{
                            color: 'text.secondary',
                          }}
                        >
                          Keys
                        </Typography>
                        <Typography>
                          {dbHealth.pebble.estimated ? '~' : ''}
                          {dbHealth.pebble.key_count.toLocaleString()}
                        </Typography>
                      </Box>
                      <Box>
                        <Typography
                          variant="caption"
                          sx={{
                            color: 'text.secondary',
                          }}
                        >
                          Size
                        </Typography>
                        <Typography>
                          {(dbHealth.pebble.size_bytes / 1024 / 1024).toFixed(1)} MB
                        </Typography>
                      </Box>
                    </Stack>
                  </Paper>
                )}

                {/* Embeddings + AI Scans + Metadata cache as grid */}
                <Grid container spacing={2}>
                  <Grid
                    size={{
                      xs: 12,
                      sm: 4,
                    }}
                  >
                    <Paper variant="outlined" sx={{ p: 2 }}>
                      <Typography
                        variant="subtitle2"
                        gutterBottom
                        sx={{
                          fontWeight: 'bold',
                        }}
                      >
                        Embeddings DB
                      </Typography>
                      <Stack spacing={0.5}>
                        <Box>
                          <Typography
                            variant="caption"
                            sx={{
                              color: 'text.secondary',
                            }}
                          >
                            Vectors
                          </Typography>
                          <Tooltip
                            title={
                              dbHealth.embeddings.estimated
                                ? `Estimate; error bound ±${(dbHealth.embeddings.error_bound_keys ?? 0).toLocaleString()} keys`
                                : ''
                            }
                          >
                            <Typography>
                              {dbHealth.embeddings.estimated ? '~' : ''}
                              {dbHealth.embeddings.vector_count.toLocaleString()}
                            </Typography>
                          </Tooltip>
                        </Box>
                        <Box>
                          <Typography
                            variant="caption"
                            sx={{
                              color: 'text.secondary',
                            }}
                          >
                            Size
                          </Typography>
                          <Typography>
                            {(dbHealth.embeddings.size_bytes / 1024 / 1024).toFixed(1)} MB
                          </Typography>
                        </Box>
                      </Stack>
                    </Paper>
                  </Grid>
                  <Grid
                    size={{
                      xs: 12,
                      sm: 4,
                    }}
                  >
                    <Paper variant="outlined" sx={{ p: 2 }}>
                      <Typography
                        variant="subtitle2"
                        gutterBottom
                        sx={{
                          fontWeight: 'bold',
                        }}
                      >
                        AI Scans DB
                      </Typography>
                      <Stack spacing={0.5}>
                        <Box>
                          <Typography
                            variant="caption"
                            sx={{
                              color: 'text.secondary',
                            }}
                          >
                            Jobs
                          </Typography>
                          <Typography>{dbHealth.ai_scans.job_count.toLocaleString()}</Typography>
                        </Box>
                        <Box>
                          <Typography
                            variant="caption"
                            sx={{
                              color: 'text.secondary',
                            }}
                          >
                            Pending
                          </Typography>
                          <Typography>
                            {dbHealth.ai_scans.pending_count.toLocaleString()}
                          </Typography>
                        </Box>
                        <Box>
                          <Typography
                            variant="caption"
                            sx={{
                              color: 'text.secondary',
                            }}
                          >
                            Size
                          </Typography>
                          <Typography>
                            {(dbHealth.ai_scans.size_bytes / 1024 / 1024).toFixed(1)} MB
                          </Typography>
                        </Box>
                      </Stack>
                    </Paper>
                  </Grid>
                  <Grid
                    size={{
                      xs: 12,
                      sm: 4,
                    }}
                  >
                    <Paper variant="outlined" sx={{ p: 2 }}>
                      <Typography
                        variant="subtitle2"
                        gutterBottom
                        sx={{
                          fontWeight: 'bold',
                        }}
                      >
                        Metadata Cache
                      </Typography>
                      <Stack spacing={0.5}>
                        <Box>
                          <Typography
                            variant="caption"
                            sx={{
                              color: 'text.secondary',
                            }}
                          >
                            Total entries
                          </Typography>
                          <Typography>
                            {dbHealth.metadata_cache.estimated ? '~' : ''}
                            {dbHealth.metadata_cache.total_entries.toLocaleString()}
                          </Typography>
                        </Box>
                        <Box>
                          <Typography
                            variant="caption"
                            sx={{
                              color: 'text.secondary',
                            }}
                          >
                            TTL
                          </Typography>
                          <Typography>
                            {dbHealth.metadata_cache.ttl_days <= 0
                              ? 'TTL off'
                              : `${dbHealth.metadata_cache.ttl_days} days`}
                          </Typography>
                        </Box>
                        <Box>
                          <Typography
                            variant="caption"
                            sx={{
                              color: 'text.secondary',
                            }}
                          >
                            Expired
                          </Typography>
                          {dbHealth.metadata_cache.expired_entries_computed === false ||
                          dbHealth.metadata_cache.expired_entries < 0 ? (
                            dbHealth.metadata_cache.ttl_days <= 0 ? (
                              <Typography sx={{ color: 'text.secondary' }}>TTL off</Typography>
                            ) : (
                              <Tooltip title="Decodes every cache row; can take a while on a large cache">
                                <span>
                                  <Button
                                    size="small"
                                    variant="outlined"
                                    onClick={countExpired}
                                    disabled={countingExpired}
                                  >
                                    {countingExpired ? 'Counting…' : 'Count expired'}
                                  </Button>
                                </span>
                              </Tooltip>
                            )
                          ) : (
                            <>
                              <Typography>
                                {dbHealth.metadata_cache.expired_entries.toLocaleString()}
                              </Typography>
                              {dbHealth.metadata_cache.expired_entries_elapsed_ms !== undefined && (
                                <Typography variant="caption" sx={{ color: 'text.secondary' }}>
                                  {`Counted in ${(
                                    dbHealth.metadata_cache.expired_entries_elapsed_ms / 1000
                                  ).toFixed(1)} s (${(
                                    dbHealth.metadata_cache.expired_entries_pages_walked ?? 0
                                  ).toLocaleString()} pages)`}
                                </Typography>
                              )}
                            </>
                          )}
                          {expiredError && (
                            <Typography variant="caption" color="error" role="alert">
                              {expiredError}
                            </Typography>
                          )}
                        </Box>
                      </Stack>
                    </Paper>
                  </Grid>
                </Grid>
              </Stack>
            )}
          </Stack>
        </AccordionDetails>
    </Accordion>
  );
}
