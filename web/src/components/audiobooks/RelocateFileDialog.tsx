// file: web/src/components/audiobooks/RelocateFileDialog.tsx
// version: 1.0.1
// guid: 8a1b2c3d-4e5f-6a7b-8c9d-0e1f2a3b4c5d

import { useState } from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  Typography,
  FormControlLabel,
  Checkbox,
  Alert,
  Box,
} from '@mui/material';
import { ServerFileBrowser } from '../common/ServerFileBrowser';
import type { BookSegment, RelocateRequest } from '../../services/api';
import * as api from '../../services/api';

interface RelocateFileDialogProps {
  open: boolean;
  onClose: () => void;
  segment: BookSegment;
  bookId: string;
  onRelocated: () => void;
}

export function RelocateFileDialog({
  open,
  onClose,
  segment,
  bookId,
  onRelocated,
}: RelocateFileDialogProps) {
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [autoSiblings, setAutoSiblings] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const handleRelocate = async () => {
    if (!selectedPath) return;
    setLoading(true);
    setError(null);
    try {
      let req: RelocateRequest;
      if (autoSiblings) {
        // Use folder mode: relocate all siblings by matching filenames
        const folderPath = selectedPath.substring(0, selectedPath.lastIndexOf('/'));
        req = { folder_path: folderPath };
      } else {
        req = { segment_id: segment.id, new_path: selectedPath };
      }
      const result = await api.relocateBookFiles(bookId, req);
      if (result.errors && result.errors.length > 0) {
        setError(result.errors.join('; '));
      } else {
        onRelocated();
        onClose();
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Relocate failed');
    } finally {
      setLoading(false);
    }
  };

  const initialDir = segment.file_path.substring(0, segment.file_path.lastIndexOf('/'));

  return (
    <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
      <DialogTitle>Relocate Missing File</DialogTitle>
      <DialogContent>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
          Current (broken) path:
        </Typography>
        <Typography
          variant="body2"
          sx={{ mb: 2, fontFamily: 'monospace', wordBreak: 'break-all', color: 'error.main' }}
        >
          {segment.file_path}
        </Typography>

        <Typography variant="body2" sx={{ mb: 1 }}>
          Browse to locate the file:
        </Typography>

        <Box
          sx={{
            border: '1px solid',
            borderColor: 'divider',
            borderRadius: 1,
            maxHeight: 400,
            overflow: 'auto',
          }}
        >
          <ServerFileBrowser
            initialPath={initialDir || '/'}
            showFiles
            allowFileSelect
            onSelect={(path, isDir) => {
              if (!isDir) setSelectedPath(path);
            }}
          />
        </Box>

        {selectedPath && (
          <Typography
            variant="body2"
            sx={{ mt: 1, fontFamily: 'monospace', wordBreak: 'break-all' }}
          >
            Selected: {selectedPath}
          </Typography>
        )}

        <FormControlLabel
          control={
            <Checkbox checked={autoSiblings} onChange={(e) => setAutoSiblings(e.target.checked)} />
          }
          label="Auto-detect siblings (relocate all missing files from this folder)"
          sx={{ mt: 1 }}
        />

        {error && (
          <Alert severity="error" sx={{ mt: 2 }}>
            {error}
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button onClick={handleRelocate} variant="contained" disabled={!selectedPath || loading}>
          {loading ? 'Relocating...' : 'Relocate'}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
