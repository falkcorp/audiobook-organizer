// file: web/src/components/settings/OpenLibraryDumps.tsx
// version: 2.2.2
// guid: e5f6a7b8-c9d0-1e2f-3a4b-5c6d7e8f9a0b

import { useState, useEffect, useCallback, useRef } from 'react';
import {
  Box,
  Button,
  LinearProgress,
  Typography,
  Alert,
  Chip,
  CircularProgress,
  Stack,
  Select,
  MenuItem,
  FormControl,
  InputLabel,
} from '@mui/material';
import CheckCircleIcon from '@mui/icons-material/CheckCircle';
import {
  getOLDumpStatus,
  startOLDumpDownload,
  startOLDumpImport,
  uploadOLDump,
  deleteOLDumpData,
  OLDumpStatus,
  OLDumpTypeStatus,
  OLDownloadProgress,
  OLUploadedFile,
} from '../../services/api';
import { useOperationsStore } from '../../stores/useOperationsStore';

function formatBytes(n: number): string {
  if (n < 0) return '?';
  if (n >= 1_073_741_824) return `${(n / 1_073_741_824).toFixed(1)} GB`;
  if (n >= 1_048_576) return `${(n / 1_048_576).toFixed(1)} MB`;
  if (n >= 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${n} B`;
}

function formatCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

function DownloadStatusChip({ dl }: { dl?: OLDownloadProgress }) {
  if (!dl || dl.status === 'idle') return null;

  if (dl.status === 'downloading') {
    const pct = dl.total_size > 0
      ? Math.round((dl.downloaded / dl.total_size) * 100)
      : null;
    return (
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
        <CircularProgress size={14} />
        <Typography variant="caption" color="text.secondary">
          {formatBytes(dl.downloaded)}
          {pct !== null ? ` (${pct}%)` : ''}
        </Typography>
      </Box>
    );
  }

  if (dl.status === 'complete') {
    return <Chip label={`Downloaded ${formatBytes(dl.downloaded)}`} size="small" color="info" />;
  }

  if (dl.status === 'error') {
    return <Chip label={`Error: ${dl.error}`} size="small" color="error" />;
  }

  return null;
}

function DumpTypeRow({
  label,
  status,
  download,
  uploadedFile,
  loading,
  importing,
}: {
  label: string;
  status?: OLDumpTypeStatus;
  download?: OLDownloadProgress;
  uploadedFile?: OLUploadedFile;
  loading?: boolean;
  importing?: boolean;
}) {
  const hasRecords = status && status.record_count > 0;
  const hasFile = !!uploadedFile;
  const isComplete = status && status.import_progress >= 1.0;
  const isInProgress = status && status.import_progress > 0 && status.import_progress < 1;

  return (
    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 0.5 }}>
      <Typography variant="body2" sx={{ minWidth: 80 }}>{label}</Typography>
      {loading ? (
        <CircularProgress size={16} />
      ) : hasRecords ? (
        <>
          <Chip
            label={`${formatCount(status.record_count)} records`}
            size="small"
            color={isComplete ? 'success' : 'info'}
            icon={isComplete ? <CheckCircleIcon /> : undefined}
          />
          {isInProgress && (
            <LinearProgress
              variant="determinate"
              value={status.import_progress * 100}
              sx={{ flexGrow: 1, maxWidth: 200 }}
            />
          )}
          {importing && !isInProgress && !isComplete && (
            <LinearProgress sx={{ flexGrow: 1, maxWidth: 200 }} />
          )}
        </>
      ) : hasFile ? (
        <>
          <Chip
            label={`Uploaded (${formatBytes(uploadedFile.size)}) - not imported`}
            size="small"
            color="warning"
          />
          {importing && (
            <LinearProgress sx={{ flexGrow: 1, maxWidth: 200 }} />
          )}
        </>
      ) : (
        <Chip label="No data" size="small" variant="outlined" />
      )}
      <DownloadStatusChip dl={download} />
    </Box>
  );
}

export function OpenLibraryDumps() {
  const [status, setStatus] = useState<OLDumpStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [downloading, setDownloading] = useState(false);
  const [importing, setImporting] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [uploadProgress, setUploadProgress] = useState(0);
  const [uploadType, setUploadType] = useState('editions');
  const fileInputRef = useRef<HTMLInputElement>(null);
  const pollRef = useRef<ReturnType<typeof setInterval>>();
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isUnmountedRef = useRef(false);

  const refresh = useCallback(async () => {
    try {
      const data = await getOLDumpStatus();
      setStatus(data);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load status');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    setLoading(true);
    refresh();
  }, [refresh]);

  // Detect running OL import on mount — read from v2 store (UOS-13).
  useEffect(() => {
    const ops = useOperationsStore.getState().activeOperations;
    const running = ops.find(
      (op) =>
        op.type === 'ol_dump_import' &&
        !['completed', 'failed', 'canceled'].includes(op.status)
    );
    if (running) {
      setImporting(true);
    }
  }, []);

  // Poll while downloading or importing
  useEffect(() => {
    const hasActiveDownload = status?.downloads && Object.values(status.downloads).some(
      d => d.status === 'downloading'
    );
    if (hasActiveDownload || importing) {
      pollRef.current = setInterval(() => {
        refresh();
        // Check if import operation is still running — read from v2 store (UOS-13).
        if (importing) {
          const ops = useOperationsStore.getState().activeOperations;
          const running = ops.find(
            (op) =>
              op.type === 'ol_dump_import' &&
              !['completed', 'failed', 'canceled'].includes(op.status)
          );
          if (!running) {
            setImporting(false);
          }
        }
      }, 3000);
    } else if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = undefined;
    }
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, [status?.downloads, importing, refresh]);

  // Cleanup download timeout on unmount
  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
    };
  }, []);

  const handleDownload = async () => {
    setDownloading(true);
    setError(null);
    try {
      await startOLDumpDownload();
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) {
          refresh();
        }
        timeoutRef.current = null;
      }, 500);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Download failed');
    } finally {
      setDownloading(false);
    }
  };

  const handleImport = async () => {
    setImporting(true);
    setError(null);
    try {
      const result = await startOLDumpImport();
      // Track in operations store for the bell indicator
      if (result?.operation_id) {
        useOperationsStore.getState().startPolling(result.operation_id, 'ol_dump_import');
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Import failed');
      setImporting(false);
    }
    // Don't set importing=false here — polling will detect when it finishes
  };

  const handleUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    setUploading(true);
    setUploadProgress(0);
    setError(null);
    try {
      await uploadOLDump(uploadType, file, (pct) => setUploadProgress(pct));
      refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Upload failed');
    } finally {
      setUploading(false);
      setUploadProgress(0);
      if (fileInputRef.current) fileInputRef.current.value = '';
    }
  };

  const handleDelete = async () => {
    if (!confirm('Delete all Open Library dump data? This cannot be undone.')) return;
    setError(null);
    try {
      await deleteOLDumpData();
      refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Delete failed');
    }
  };

  const dl = status?.downloads || {};
  const uf = status?.uploaded_files || {};

  return (
    <Box>
      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

      <Box sx={{ mb: 2 }}>
        <DumpTypeRow label="Editions" status={status?.status?.editions} download={dl.editions} uploadedFile={uf.editions} loading={loading} importing={importing} />
        <DumpTypeRow label="Authors" status={status?.status?.authors} download={dl.authors} uploadedFile={uf.authors} loading={loading} importing={importing} />
        <DumpTypeRow label="Works" status={status?.status?.works} download={dl.works} uploadedFile={uf.works} loading={loading} importing={importing} />
      </Box>

      <Stack direction="row" spacing={1} sx={{ flexWrap: 'wrap', gap: 1 }}>
        <Button
          variant="contained"
          size="small"
          onClick={handleDownload}
          disabled={downloading}
        >
          {downloading ? 'Starting...' : 'Download Latest Dumps'}
        </Button>
        <Button
          variant="outlined"
          size="small"
          onClick={handleImport}
          disabled={importing}
        >
          {importing ? 'Importing...' : 'Import to Local DB'}
        </Button>
        <Button
          variant="outlined"
          size="small"
          color="error"
          onClick={handleDelete}
          disabled={importing}
        >
          Delete Data
        </Button>
        <Button variant="text" size="small" onClick={refresh}>
          Refresh
        </Button>
      </Stack>

      <Box sx={{ mt: 2, display: 'flex', alignItems: 'center', gap: 1 }}>
        <Typography variant="body2" color="text.secondary">
          Manual upload:
        </Typography>
        <FormControl size="small" sx={{ minWidth: 120 }}>
          <InputLabel>Type</InputLabel>
          <Select
            value={uploadType}
            label="Type"
            onChange={(e) => setUploadType(e.target.value)}
          >
            <MenuItem value="editions">Editions</MenuItem>
            <MenuItem value="authors">Authors</MenuItem>
            <MenuItem value="works">Works</MenuItem>
          </Select>
        </FormControl>
        <Button
          variant="outlined"
          size="small"
          component="label"
          disabled={uploading}
        >
          {uploading ? 'Uploading...' : 'Upload .txt.gz'}
          <input
            type="file"
            hidden
            accept=".gz"
            ref={fileInputRef}
            onChange={handleUpload}
          />
        </Button>
      </Box>

      {uploading && (
        <Box sx={{ mt: 1, display: 'flex', alignItems: 'center', gap: 1 }}>
          <LinearProgress
            variant="determinate"
            value={uploadProgress}
            sx={{ flexGrow: 1, maxWidth: 300 }}
          />
          <Typography variant="caption" color="text.secondary">
            {uploadProgress}%
          </Typography>
        </Box>
      )}
    </Box>
  );
}
