// file: web/src/components/ChangeLog.tsx
// version: 1.5.0
// guid: 00f575de-ecea-45b7-9aa5-d6dbbc3f21f6

import { useCallback, useEffect, useState } from 'react';
import {
  Box,
  Button,
  CircularProgress,
  Stack,
  Typography,
} from '@mui/material';
import RestoreIcon from '@mui/icons-material/Restore';
import type { ChangeLogEntry } from '../services/api';
import * as api from '../services/api';
import { fetchActivity } from '../services/activityApi';
import type { ActivityEntry } from '../services/activityApi';

interface ChangeLogProps {
  bookId: string;
  refreshKey?: number;
  onRevert?: () => void; // called after successful revert so parent can refresh
  onCompareSnapshot?: (timestamp: string) => void; // called when user clicks "Compare →" on a tag_write entry
}

const TYPE_ICONS: Record<string, string> = {
  tag_write: '\uD83C\uDFF7\uFE0F',     // label/tag
  rename: '\uD83D\uDCC1',              // folder
  metadata_apply: '\uD83D\uDCE5',      // inbox tray
  import: '\uD83D\uDCE6',              // package
  transcode: '\uD83D\uDD04',           // arrows cycle
};

const TYPE_LABELS: Record<string, string> = {
  tag_write: 'Tag Write',
  rename: 'Rename',
  metadata_apply: 'Metadata Apply',
  import: 'Import',
  transcode: 'Transcode',
};

const formatTimestamp = (ts: string): string => {
  const date = new Date(ts);
  if (isNaN(date.getTime())) return ts;
  return date.toLocaleString();
};

export const ChangeLog = ({ bookId, refreshKey, onRevert, onCompareSnapshot }: ChangeLogProps) => {
  const [entries, setEntries] = useState<ChangeLogEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [reverting, setReverting] = useState<string | null>(null);

  const mapActivityToChangeLogEntry = (a: ActivityEntry): ChangeLogEntry => ({
    timestamp: a.timestamp,
    type: a.type as ChangeLogEntry['type'],
    summary: a.summary,
    details: a.details,
  });

  const loadChangelog = useCallback(async () => {
    setLoading(true);
    try {
      // Try the unified activity log first; fall back to the legacy endpoint
      try {
        const result = await fetchActivity({ book_id: bookId, tier: 'change', limit: 50 });
        if (result.entries && result.entries.length > 0) {
          setEntries(result.entries.map(mapActivityToChangeLogEntry));
          return;
        }
      } catch {
        // Fall through to legacy endpoint
      }
      const result = await api.getBookChangelog(bookId);
      setEntries(result.entries || []);
    } catch {
      setEntries([]);
    } finally {
      setLoading(false);
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bookId]);

  useEffect(() => {
    loadChangelog();
  }, [loadChangelog, refreshKey]);

  const handleRevert = async (timestamp: string) => {
    setReverting(timestamp);
    try {
      const revertResp = await fetch(`/api/v1/audiobooks/${bookId}/revert-metadata`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ timestamp }),
      });
      if (!revertResp.ok) {
        console.error('Revert failed:', revertResp.status, await revertResp.text());
        return;
      }
      // Also trigger write-back to sync tags to file
      const wbResp = await fetch(`/api/v1/audiobooks/${bookId}/write-back`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ rename: true }),
      });
      if (!wbResp.ok) {
        console.error('Write-back after revert failed:', wbResp.status);
      }
      loadChangelog();
      onRevert?.();
    } catch (err) {
      console.error('Revert failed:', err);
    } finally {
      setReverting(null);
    }
  };

  if (loading) {
    return (
      <Box sx={{ display: 'flex', justifyContent: 'center', py: 3 }}>
        <CircularProgress size={24} />
      </Box>
    );
  }

  if (entries.length === 0) {
    return (
      <Typography variant="body2" color="text.secondary" sx={{ py: 2 }} data-testid="changelog-empty">
        No changes recorded yet.
      </Typography>
    );
  }

  return (
    <Stack spacing={0} data-testid="changelog-timeline">
      {entries.map((entry, idx) => (
        <Box
          key={`${entry.timestamp}-${idx}`}
          sx={[{
            display: 'flex',
            alignItems: 'flex-start',
            gap: 2,
            py: 1.5,
            px: 1,
            borderColor: 'divider',
            cursor: (entry.type === 'metadata_apply' || entry.type === 'tag_write') ? 'pointer' : undefined,
            '&:hover': { bgcolor: 'action.hover' }
          }, idx < entries.length - 1 ? {
            borderBottom: '1px solid'
          } : {
            borderBottom: 'none'
          }]}
          onClick={() => {
            if (entry.type === 'metadata_apply' || entry.type === 'tag_write') {
              onCompareSnapshot?.(entry.timestamp);
            }
          }}
        >
          {/* Timestamp */}
          <Typography
            variant="caption"
            color="text.secondary"
            sx={{ minWidth: 140, flexShrink: 0, pt: 0.25 }}
          >
            {formatTimestamp(entry.timestamp)}
          </Typography>

          {/* Icon + summary */}
          <Stack direction="row" spacing={1} alignItems="center" sx={{ flex: 1, minWidth: 0 }}>
            <Typography variant="body2" sx={{ flexShrink: 0 }}>
              {TYPE_ICONS[entry.type] || '\u2022'}
            </Typography>
            <Box sx={{ flex: 1, minWidth: 0 }}>
              <Typography variant="body2" sx={{ fontWeight: 500 }}>
                {TYPE_LABELS[entry.type] || entry.type}
              </Typography>
              <Typography variant="body2" color="text.secondary" noWrap>
                {entry.summary}
              </Typography>
            </Box>
          </Stack>

          {/* Actions */}
          <Stack direction="row" spacing={1} sx={{ flexShrink: 0, alignItems: 'center' }}>
            {idx > 0 && (entry.type === 'metadata_apply' || entry.type === 'tag_write' || entry.type === 'rename') && (
              <Button
                size="small"
                variant="outlined"
                color="warning"
                startIcon={<RestoreIcon />}
                disabled={reverting === entry.timestamp}
                onClick={(e) => { e.stopPropagation(); handleRevert(entry.timestamp); }}
                sx={{ fontSize: '0.7rem', py: 0.25, px: 1 }}
              >
                {reverting === entry.timestamp ? 'Reverting...' : 'Revert'}
              </Button>
            )}
          </Stack>
        </Box>
      ))}
    </Stack>
  );
};
