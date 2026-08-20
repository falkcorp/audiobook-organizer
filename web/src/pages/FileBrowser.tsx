// file: web/src/pages/FileBrowser.tsx
// version: 1.0.2
// guid: 2c1d3e4f-5a6b-7c8d-9e0f-1a2b3c4d5e6f

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Box, Button, Typography, Alert, Stack } from '@mui/material';
import { Add as AddIcon } from '@mui/icons-material';
import { ServerFileBrowser } from '../components/common/ServerFileBrowser';
import * as api from '../services/api';

export function FileBrowser() {
  const navigate = useNavigate();
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
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
        initialPath="/"
        onSelect={(path, isDir) => {
          if (isDir) {
            setSelectedPath(path);
          }
        }}
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
