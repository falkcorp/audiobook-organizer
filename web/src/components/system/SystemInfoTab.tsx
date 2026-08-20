// file: web/src/components/system/SystemInfoTab.tsx
// version: 1.6.2
// guid: 1a2b3c4d-5e6f-7a8b-9c0d-1e2f3a4b5c6d

import { useState, useEffect } from 'react';
import {
  Box,
  Typography,
  Grid,
  Card,
  CardContent,
  Chip,
  Stack,
  Divider,
  Button,
  CircularProgress,
} from '@mui/material';
import {
  Computer as ComputerIcon,
  Memory as MemoryIcon,
  Storage as StorageIcon,
  Code as CodeIcon,
  Refresh as RefreshIcon,
  Timer as TimerIcon,
} from '@mui/icons-material';
import * as api from '../../services/api';

interface SystemInfo {
  version: string;
  os: {
    platform: string;
    arch: string;
    version: string;
  };
  memory: {
    total: number;
    used: number;
    free: number;
    heapAlloc: number;
    heapSys: number;
  };
  cpu: {
    cores: number;
  };
  runtime: {
    goVersion: string;
    numGoroutines: number;
  };
  database: {
    size: number;
    books: number;
    folderCount: number;
  };
  uptime: {
    appSeconds: number;
    systemSeconds: number;
  };
}

export function SystemInfoTab() {
  const [info, setInfo] = useState<SystemInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    fetchSystemInfo();
  }, []);

  const fetchSystemInfo = async () => {
    setLoading(true);
    setError(null);
    try {
      const status = await api.getSystemStatus();
      const librarySize = status.library_size_bytes ?? status.library.total_size;
      const libraryBooks = status.library_book_count ?? status.library.book_count;
      const folderCount = status.import_paths?.folder_count ?? 0;

      // Map API SystemStatus to SystemInfo format
      setInfo({
        version: status.version || 'unknown',
        os: {
          platform: status.runtime.os,
          arch: status.runtime.arch,
          version: `${status.runtime.os} ${status.runtime.arch}`,
        },
        memory: {
          total: status.memory.sys_bytes,
          used: status.memory.alloc_bytes,
          free: status.memory.sys_bytes - status.memory.alloc_bytes,
          heapAlloc: status.memory.heap_alloc,
          heapSys: status.memory.heap_sys,
        },
        cpu: {
          cores: status.runtime.num_cpu,
        },
        runtime: {
          goVersion: status.runtime.go_version,
          numGoroutines: status.runtime.num_goroutine,
        },
        database: {
          size: librarySize,
          books: libraryBooks,
          folderCount,
        },
        uptime: {
          appSeconds: status.app_uptime_seconds ?? 0,
          systemSeconds: status.system_uptime_seconds ?? 0,
        },
      });
    } catch (err) {
      console.error('Failed to fetch system info:', err);
      setError(err instanceof Error ? err.message : 'Failed to fetch system info');
    } finally {
      setLoading(false);
    }
  };

  const formatBytes = (bytes: number): string => {
    const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB'];
    if (bytes === 0) return '0 Bytes';
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    return `${(bytes / Math.pow(1024, i)).toFixed(2)} ${sizes[i]}`;
  };

  const formatUptime = (totalSeconds: number): string => {
    if (totalSeconds <= 0) return 'unknown';
    const d = Math.floor(totalSeconds / 86400);
    const h = Math.floor((totalSeconds % 86400) / 3600);
    const m = Math.floor((totalSeconds % 3600) / 60);
    const s = Math.floor(totalSeconds % 60);
    const parts: string[] = [];
    if (d > 0) parts.push(`${d}d`);
    if (h > 0) parts.push(`${h}h`);
    if (m > 0) parts.push(`${m}m`);
    parts.push(`${s}s`);
    return parts.join(' ');
  };

  const getOSIcon = () => {
    if (!info) return '💻';
    switch (info.os.platform) {
      case 'darwin':
        return '🍎';
      case 'linux':
        return '🐧';
      case 'windows':
        return '🪟';
      default:
        return '💻';
    }
  };

  if (loading) {
    return (
      <Box
        sx={{
          display: 'flex',
          justifyContent: 'center',
          alignItems: 'center',
          minHeight: '400px',
        }}
      >
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return (
      <Box
        sx={{
          display: 'flex',
          justifyContent: 'center',
          alignItems: 'center',
          minHeight: '400px',
        }}
      >
        <Typography color="error">{error}</Typography>
      </Box>
    );
  }

  if (!info) {
    return null;
  }

  return (
    <Box>
      <Stack
        direction="row"
        sx={{
          justifyContent: 'space-between',
          alignItems: 'center',
          mb: 2,
        }}
      >
        <Stack
          direction="row"
          spacing={2}
          sx={{
            alignItems: 'center',
          }}
        >
          <Typography variant="h6">System Information</Typography>
          <Chip label={`v${info.version}`} size="small" color="primary" />
        </Stack>
        <Button
          variant="outlined"
          startIcon={<RefreshIcon />}
          onClick={fetchSystemInfo}
          disabled={loading}
        >
          Refresh
        </Button>
      </Stack>

      <Grid container spacing={3}>
        {/* Operating System */}
        <Grid
          size={{
            xs: 12,
            md: 6,
          }}
        >
          <Card>
            <CardContent>
              <Stack
                direction="row"
                spacing={2}
                sx={{
                  alignItems: 'center',
                  mb: 2,
                }}
              >
                <ComputerIcon color="primary" />
                <Typography variant="h6">Operating System</Typography>
              </Stack>
              <Stack spacing={1.5}>
                <Box>
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    Platform
                  </Typography>
                  <Stack
                    direction="row"
                    spacing={1}
                    sx={{
                      alignItems: 'center',
                    }}
                  >
                    <Typography variant="body1">{getOSIcon()}</Typography>
                    <Typography variant="body1">{info.os.version}</Typography>
                  </Stack>
                </Box>
                <Divider />
                <Box>
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    Architecture
                  </Typography>
                  <Typography variant="body1">{info.os.arch}</Typography>
                </Box>
              </Stack>
            </CardContent>
          </Card>
        </Grid>

        {/* Memory */}
        <Grid
          size={{
            xs: 12,
            md: 6,
          }}
        >
          <Card>
            <CardContent>
              <Stack
                direction="row"
                spacing={2}
                sx={{
                  alignItems: 'center',
                  mb: 2,
                }}
              >
                <MemoryIcon color="primary" />
                <Typography variant="h6">Memory</Typography>
              </Stack>
              <Stack spacing={1.5}>
                <Box>
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    App Memory System
                  </Typography>
                  <Typography variant="body1">{formatBytes(info.memory.total)}</Typography>
                </Box>
                <Divider />
                <Box>
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    Used by App
                  </Typography>
                  <Typography variant="body1">{formatBytes(info.memory.used)}</Typography>
                </Box>
                <Divider />
                <Box>
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    Heap Allocated
                  </Typography>
                  <Typography variant="body1">{formatBytes(info.memory.heapAlloc)}</Typography>
                </Box>
                <Divider />
                <Box>
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    Heap System
                  </Typography>
                  <Typography variant="body1">{formatBytes(info.memory.heapSys)}</Typography>
                </Box>
              </Stack>
            </CardContent>
          </Card>
        </Grid>

        {/* CPU */}
        <Grid
          size={{
            xs: 12,
            md: 6,
          }}
        >
          <Card>
            <CardContent>
              <Stack
                direction="row"
                spacing={2}
                sx={{
                  alignItems: 'center',
                  mb: 2,
                }}
              >
                <ComputerIcon color="primary" />
                <Typography variant="h6">CPU</Typography>
              </Stack>
              <Stack spacing={1.5}>
                <Box>
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    Available Cores
                  </Typography>
                  <Typography variant="body1">{info.cpu.cores}</Typography>
                </Box>
              </Stack>
            </CardContent>
          </Card>
        </Grid>

        {/* Uptime */}
        <Grid
          size={{
            xs: 12,
            md: 6,
          }}
        >
          <Card>
            <CardContent>
              <Stack
                direction="row"
                spacing={2}
                sx={{
                  alignItems: 'center',
                  mb: 2,
                }}
              >
                <TimerIcon color="primary" />
                <Typography variant="h6">Uptime</Typography>
              </Stack>
              <Stack spacing={1.5}>
                <Box>
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    Application
                  </Typography>
                  <Typography variant="body1">{formatUptime(info.uptime.appSeconds)}</Typography>
                </Box>
                <Divider />
                <Box>
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    System
                  </Typography>
                  <Typography variant="body1">{formatUptime(info.uptime.systemSeconds)}</Typography>
                </Box>
              </Stack>
            </CardContent>
          </Card>
        </Grid>

        {/* Runtime */}
        <Grid
          size={{
            xs: 12,
            md: 6,
          }}
        >
          <Card>
            <CardContent>
              <Stack
                direction="row"
                spacing={2}
                sx={{
                  alignItems: 'center',
                  mb: 2,
                }}
              >
                <CodeIcon color="primary" />
                <Typography variant="h6">Runtime</Typography>
              </Stack>
              <Stack spacing={1.5}>
                <Box>
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    Go Version
                  </Typography>
                  <Typography variant="body1">{info.runtime.goVersion}</Typography>
                </Box>
                <Divider />
                <Box>
                  <Typography
                    variant="body2"
                    sx={{
                      color: 'text.secondary',
                    }}
                  >
                    Goroutines
                  </Typography>
                  <Typography variant="body1">{info.runtime.numGoroutines}</Typography>
                </Box>
              </Stack>
            </CardContent>
          </Card>
        </Grid>

        {/* Database & Library */}
        <Grid size={12}>
          <Card>
            <CardContent>
              <Stack
                direction="row"
                spacing={2}
                sx={{
                  alignItems: 'center',
                  mb: 2,
                }}
              >
                <StorageIcon color="primary" />
                <Typography variant="h6">Library & Storage</Typography>
              </Stack>
              <Grid container spacing={3}>
                <Grid
                  size={{
                    xs: 12,
                    sm: 6,
                    md: 4,
                  }}
                >
                  <Box>
                    <Typography
                      variant="body2"
                      sx={{
                        color: 'text.secondary',
                      }}
                    >
                      Library Size
                    </Typography>
                    <Typography variant="h6">{formatBytes(info.database.size)}</Typography>
                  </Box>
                </Grid>
                <Grid
                  size={{
                    xs: 12,
                    sm: 6,
                    md: 4,
                  }}
                >
                  <Box>
                    <Typography
                      variant="body2"
                      sx={{
                        color: 'text.secondary',
                      }}
                    >
                      Books
                    </Typography>
                    <Typography variant="h6">{info.database.books.toLocaleString()}</Typography>
                  </Box>
                </Grid>
                <Grid
                  size={{
                    xs: 12,
                    sm: 6,
                    md: 4,
                  }}
                >
                  <Box>
                    <Typography
                      variant="body2"
                      sx={{
                        color: 'text.secondary',
                      }}
                    >
                      Import Paths
                    </Typography>
                    <Typography variant="h6">{info.database.folderCount}</Typography>
                  </Box>
                </Grid>
              </Grid>
            </CardContent>
          </Card>
        </Grid>
      </Grid>
    </Box>
  );
}
