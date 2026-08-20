// file: web/src/components/system/QuotaTab.tsx
// version: 1.1.1
// guid: 0f1a2b3c-4d5e-6f7a-8b9c-0d1e2f3a4b5c

import { useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Chip,
  CircularProgress,
  Grid,
  LinearProgress,
  Stack,
  Typography,
} from '@mui/material';
import {
  CheckCircle as CheckIcon,
  Refresh as RefreshIcon,
  Warning as WarningIcon,
} from '@mui/icons-material';
import * as api from '../../services/api';

interface QuotaInfo {
  path: string;
  systemQuotaEnabled: boolean;
  systemQuotaPercent: number;
  systemQuotaUsed: number;
  systemQuotaLimit: number;
  userQuotasEnabled: boolean;
}

export function QuotaTab() {
  const [quota, setQuota] = useState<QuotaInfo | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchQuotaInfo = async () => {
    setLoading(true);
    setError(null);
    try {
      const [storage, config] = await Promise.all([api.getSystemStorage(), api.getConfig()]);

      const enabled = Boolean(config.enable_disk_quota);
      const quotaPercent = config.disk_quota_percent || 80;
      const quotaLimit = enabled
        ? Math.floor(storage.total_bytes * (quotaPercent / 100))
        : storage.total_bytes;

      setQuota({
        path: storage.path,
        systemQuotaEnabled: enabled,
        systemQuotaPercent: quotaPercent,
        systemQuotaUsed: storage.used_bytes,
        systemQuotaLimit: quotaLimit,
        userQuotasEnabled: Boolean(config.enable_user_quotas),
      });
    } catch (fetchError) {
      setError(fetchError instanceof Error ? fetchError.message : 'Failed to fetch quota info');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void fetchQuotaInfo();
  }, []);

  const formatBytes = (bytes: number): string => {
    const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB'];
    if (!bytes) return '0 Bytes';
    const index = Math.floor(Math.log(bytes) / Math.log(1024));
    const value = bytes / Math.pow(1024, index);
    return `${value.toFixed(2)} ${sizes[index]}`;
  };

  if (loading) {
    return (
      <Box display="flex" justifyContent="center" alignItems="center" minHeight="320px">
        <CircularProgress />
      </Box>
    );
  }

  if (error) {
    return (
      <Box>
        <Alert severity="error">{error}</Alert>
      </Box>
    );
  }

  if (!quota) {
    return null;
  }

  const percentage =
    quota.systemQuotaLimit > 0 ? (quota.systemQuotaUsed / quota.systemQuotaLimit) * 100 : 0;

  const progressColor: 'primary' | 'warning' | 'error' =
    percentage >= 100 ? 'error' : percentage >= 85 ? 'warning' : 'primary';

  return (
    <Box>
      <Stack direction="row" justifyContent="space-between" alignItems="center" mb={2}>
        <Typography variant="h6">Quota Information</Typography>
        <Button
          variant="outlined"
          startIcon={<RefreshIcon />}
          onClick={() => {
            void fetchQuotaInfo();
          }}
          disabled={loading}
        >
          Refresh
        </Button>
      </Stack>

      <Grid container spacing={3}>
        <Grid size={12}>
          <Card>
            <CardContent>
              <Stack direction="row" justifyContent="space-between" alignItems="center" mb={2}>
                <Typography variant="h6">System-wide Quota</Typography>
                <Chip
                  label={quota.systemQuotaEnabled ? 'Enabled' : 'Disabled'}
                  color={quota.systemQuotaEnabled ? 'primary' : 'default'}
                  size="small"
                />
              </Stack>

              <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
                Path: {quota.path}
              </Typography>

              {quota.systemQuotaEnabled ? (
                <>
                  {percentage >= 100 && (
                    <Alert severity="error" sx={{ mb: 2 }}>
                      Storage exceeds configured quota limit.
                    </Alert>
                  )}
                  {percentage >= 85 && percentage < 100 && (
                    <Alert icon={<WarningIcon />} severity="warning" sx={{ mb: 2 }}>
                      Approaching configured quota limit.
                    </Alert>
                  )}

                  <Stack direction="row" justifyContent="space-between" mb={1}>
                    <Typography variant="body2" color="text.secondary">
                      {formatBytes(quota.systemQuotaUsed)} used of{' '}
                      {formatBytes(quota.systemQuotaLimit)}
                    </Typography>
                    <Typography variant="body2" color="text.secondary">
                      {percentage.toFixed(1)}%
                    </Typography>
                  </Stack>
                  <LinearProgress
                    variant="determinate"
                    value={Math.min(percentage, 100)}
                    color={progressColor}
                    sx={{ height: 10, borderRadius: 1, mb: 2 }}
                  />
                  <Typography variant="body2" color="text.secondary">
                    Maximum disk usage is limited to {quota.systemQuotaPercent}% of total available
                    space.
                  </Typography>
                </>
              ) : (
                <Stack direction="row" spacing={1} alignItems="center">
                  <CheckIcon color="success" />
                  <Typography variant="body2" color="text.secondary">
                    Disk quota is disabled. Available storage is unrestricted.
                  </Typography>
                </Stack>
              )}
            </CardContent>
          </Card>
        </Grid>

        <Grid size={12}>
          <Card>
            <CardContent>
              <Typography variant="h6" gutterBottom>
                Per-user Quotas
              </Typography>
              <Typography variant="body2" color="text.secondary">
                {quota.userQuotasEnabled
                  ? 'Per-user quotas are enabled. Detailed per-user usage reporting is not yet available in this view.'
                  : 'Per-user quotas are disabled.'}
              </Typography>
            </CardContent>
          </Card>
        </Grid>
      </Grid>
    </Box>
  );
}
