// file: web/src/components/PendingFileOpsBanner.tsx
// version: 1.0.1
// guid: 6c1e8b3d-2a47-4f5d-9e0c-b8a4d2f6e7a3
//
// Banner shown when background file operations (tag write, cover embed,
// rename) are in flight. Mounted at the top of the Activity Log so users
// can see what's still happening before the activity entries are written.

import { useState } from 'react';
import {
  Box,
  CircularProgress,
  Collapse,
  IconButton,
  LinearProgress,
  Paper,
  Stack,
  Typography,
} from '@mui/material';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import ExpandLessIcon from '@mui/icons-material/ExpandLess';
import type { PendingFileOp } from '../services/fileOpsApi';

interface Props {
  operations: PendingFileOp[];
}

function formatOpType(t: string): string {
  switch (t) {
    case 'apply_metadata':
      return 'Writing tags & cover';
    case 'tag_writeback':
      return 'Writing tags';
    case 'cover_embed':
      return 'Embedding cover';
    case 'rename':
      return 'Renaming files';
    default:
      return t.replace(/_/g, ' ');
  }
}

function timeAgo(iso: string): string {
  const sec = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  if (m < 60) return `${m}m`;
  return `${Math.floor(m / 60)}h`;
}

export function PendingFileOpsBanner({ operations }: Props): JSX.Element | null {
  const [expanded, setExpanded] = useState(false);
  if (operations.length === 0) return null;

  const count = operations.length;

  return (
    <Paper variant="outlined" sx={{ mb: 2, borderColor: 'info.main' }}>
      <Box
        sx={{
          display: 'flex',
          alignItems: 'center',
          gap: 1.5,
          px: 2,
          py: 1.25,
          cursor: 'pointer',
        }}
        onClick={() => setExpanded((v) => !v)}
      >
        <CircularProgress size={18} thickness={5} />
        <Typography variant="body2" sx={{ flex: 1 }}>
          Writing files for <strong>{count}</strong> book{count === 1 ? '' : 's'}
          {count > 1 && '...'}
        </Typography>
        <IconButton size="small">
          {expanded ? <ExpandLessIcon /> : <ExpandMoreIcon />}
        </IconButton>
      </Box>
      <LinearProgress sx={{ height: 2 }} />
      <Collapse in={expanded}>
        <Stack divider={<Box sx={{ borderBottom: '1px solid', borderColor: 'divider' }} />}>
          {operations.map((op) => (
            <Box
              key={`${op.book_id}:${op.op_type}`}
              sx={{ px: 2, py: 0.75, display: 'flex', alignItems: 'center', gap: 1.5 }}
            >
              <Typography variant="body2" sx={{ flex: 1 }} noWrap>
                {op.book_title || op.book_id}
              </Typography>
              <Typography variant="caption" color="text.secondary">
                {formatOpType(op.op_type)}
              </Typography>
              <Typography variant="caption" color="text.secondary" sx={{ minWidth: 32, textAlign: 'right' }}>
                {timeAgo(op.started_at)}
              </Typography>
            </Box>
          ))}
        </Stack>
      </Collapse>
    </Paper>
  );
}
