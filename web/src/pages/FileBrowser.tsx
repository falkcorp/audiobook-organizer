// file: web/src/pages/FileBrowser.tsx
// version: 1.2.1
// guid: 2c1d3e4f-5a6b-7c8d-9e0f-1a2b3c4d5e6f
// last-edited: 2026-11-07

import { useCallback, useEffect, useRef, useState } from 'react';
import { useNavigate, useSearchParams } from 'react-router';
import { applyOwnedParams } from '../hooks/useUrlState';
import { Box, Button, Typography, Alert, Stack } from '@mui/material';
import { Add as AddIcon } from '@mui/icons-material';
import { ServerFileBrowser } from '../components/common/ServerFileBrowser';
import * as api from '../services/api';

export function FileBrowser() {
  const navigate = useNavigate();
  // The directory on screen is `?path=`: the browser opens there, every directory
  // it lands on is written back (so the address bar can be copied), and a path the
  // address bar changes under us (a pasted link, back/forward) is opened. The root
  // is the default and is omitted.
  const [searchParams, setSearchParams] = useSearchParams();
  const urlPath = searchParams.get('path') || '/';
  const [startPath, setStartPath] = useState(urlPath);
  // The browser owns its location after it mounts, so an outside change is
  // delivered by mounting it again at the new path (its fetch is the only side
  // effect, and it is the one fetch the new directory needs anyway).
  const [browserKey, setBrowserKey] = useState(0);
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  // onSelect must keep ONE identity: ServerFileBrowser's directory fetch depends
  // on it, so a new function per render (or per URL change) refetches the folder
  // it just listed. The setter and the last path written live in refs for that.
  const setParamsRef = useRef(setSearchParams);
  useEffect(() => {
    setParamsRef.current = setSearchParams;
  });
  const writtenPath = useRef(urlPath);
  // A path in the URL that we did not write is an outside change.
  useEffect(() => {
    if (urlPath === writtenPath.current) return;
    writtenPath.current = urlPath;
    setStartPath(urlPath);
    setSelectedPath(null);
    setBrowserKey((k) => k + 1);
  }, [urlPath]);
  const handleSelect = useCallback((path: string, isDir: boolean) => {
    if (!isDir) return;
    setSelectedPath(path);
    // Write only a CHANGE: a write of the same address still re-renders this page,
    // which would hand the browser a new onSelect and loop.
    if (path === writtenPath.current) return;
    writtenPath.current = path;
    setParamsRef.current((prev) => applyOwnedParams(prev, { path }, { path: '/' }), {
      replace: true,
    });
  }, []);
  const [notice, setNotice] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  const handleAddImportPath = async () => {
    if (!selectedPath) return;
    setAdding(true);
    setNotice(null);
    try {
      await api.addImportPath(selectedPath, selectedPath.split('/').pop() || '');
      setNotice('Import path added successfully.');
      navigate('/settings');
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to add import path';
      setNotice(message);
    } finally {
      setAdding(false);
    }
  };

  return (
    <Box sx={{ height: '100%', overflow: 'auto' }}>
      <Stack
        direction={{ xs: 'column', sm: 'row' }}
        spacing={2}
        sx={{
          alignItems: { xs: 'flex-start', sm: 'center' },
          justifyContent: 'space-between',
          mb: 2,
        }}
      >
        <Typography variant="h4">File Browser</Typography>
        <Button
          variant="contained"
          startIcon={<AddIcon />}
          onClick={handleAddImportPath}
          disabled={!selectedPath || adding}
        >
          {adding ? 'Adding...' : 'Add as Import Path'}
        </Button>
      </Stack>
      {notice && (
        <Alert severity="info" sx={{ mb: 2 }}>
          {notice}
        </Alert>
      )}
      <ServerFileBrowser
        key={browserKey}
        initialPath={startPath}
        onSelect={handleSelect}
        showFiles
        allowDirSelect
        allowFileSelect
      />
      {selectedPath && (
        <Alert severity="success" sx={{ mt: 2 }}>
          <strong>Selected:</strong> {selectedPath}
        </Alert>
      )}
    </Box>
  );
}
