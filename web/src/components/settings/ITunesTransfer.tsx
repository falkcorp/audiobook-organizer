// file: web/src/components/settings/ITunesTransfer.tsx
// version: 2.1.0
// guid: 5e6f7a8b-9c0d-1e2f-3a4b-5c6d7e8f9a0b
// last-edited: 2026-10-10
//
// Download of the configured iTunes Library.itl. Upload, install, backup
// listing and restore were removed with iTunes write-back on 2026-10-07:
// iTunes is an import-only source, so nothing here writes the library.

import { useState } from 'react';
import {
  Box,
  Button,
  Card,
  CardContent,
  CardHeader,
  CircularProgress,
  Typography,
} from '@mui/material';
import CloudDownloadIcon from '@mui/icons-material/CloudDownload';
import { useToast } from '../toast/ToastProvider';
import { apiFetch } from '../../utils/apiFetch';

const API_BASE = '/api/v1/itunes/library';

async function downloadITL(): Promise<void> {
  const resp = await apiFetch(`${API_BASE}/download`);
  if (!resp.ok) {
    const body = await resp.json().catch(() => ({ error: resp.statusText }));
    throw new Error(body.error || `Download failed: ${resp.status}`);
  }
  const blob = await resp.blob();
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = 'iTunes Library.itl';
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

export function ITunesTransfer() {
  const { toast } = useToast();
  const [downloading, setDownloading] = useState(false);

  const handleDownload = async () => {
    setDownloading(true);
    try {
      await downloadITL();
      toast('ITL file downloaded', 'success');
    } catch (err) {
      toast(`Download failed: ${err}`, 'error');
    } finally {
      setDownloading(false);
    }
  };

  return (
    <Card sx={{ mt: 3 }}>
      <CardHeader title="ITL File Download" subheader="Download the configured iTunes Library file" />
      <CardContent>
        <Box>
          <Typography
            variant="body2"
            sx={{
              color: 'text.secondary',
              mb: 1,
            }}
          >
            Download the current ITL file from the server. iTunes is an import-only source: the
            organizer never writes this file.
          </Typography>
          <Button
            variant="outlined"
            startIcon={downloading ? <CircularProgress size={18} /> : <CloudDownloadIcon />}
            onClick={handleDownload}
            disabled={downloading}
          >
            {downloading ? 'Downloading...' : 'Download Library'}
          </Button>
        </Box>
      </CardContent>
    </Card>
  );
}
