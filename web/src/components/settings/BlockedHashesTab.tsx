// file: web/src/components/settings/BlockedHashesTab.tsx
// version: 1.0.1

import { useEffect, useState } from 'react';
import {
  Box,
  Typography,
  Button,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  IconButton,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Alert,
  Snackbar,
} from '@mui/material';
import { Delete as DeleteIcon, Add as AddIcon, Block as BlockIcon } from '@mui/icons-material';
import {
  getBlockedHashes,
  addBlockedHash,
  removeBlockedHash,
  BlockedHash,
} from '../../services/api';

export default function BlockedHashesTab() {
  const [hashes, setHashes] = useState<BlockedHash[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [addDialogOpen, setAddDialogOpen] = useState(false);
  const [deleteDialogOpen, setDeleteDialogOpen] = useState(false);
  const [selectedHash, setSelectedHash] = useState<string | null>(null);
  const [newHash, setNewHash] = useState('');
  const [newReason, setNewReason] = useState('');
  const [snackbar, setSnackbar] = useState<{
    open: boolean;
    message: string;
    severity: 'success' | 'error';
  }>({
    open: false,
    message: '',
    severity: 'success',
  });

  useEffect(() => {
    loadBlockedHashes();
  }, []);

  const loadBlockedHashes = async () => {
    try {
      setLoading(true);
      setError(null);
      const response = await getBlockedHashes();
      setHashes(response.items || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load blocked hashes');
    } finally {
      setLoading(false);
    }
  };

  const handleAddHash = async () => {
    if (!newHash || !newReason) {
      setSnackbar({
        open: true,
        message: 'Hash and reason are required',
        severity: 'error',
      });
      return;
    }

    // Validate hash format (64 character hex for SHA256)
    if (!/^[a-fA-F0-9]{64}$/.test(newHash)) {
      setSnackbar({
        open: true,
        message: 'Hash must be 64 hexadecimal characters (SHA256)',
        severity: 'error',
      });
      return;
    }

    try {
      await addBlockedHash(newHash, newReason);
      setSnackbar({
        open: true,
        message: 'Hash blocked successfully',
        severity: 'success',
      });
      setAddDialogOpen(false);
      setNewHash('');
      setNewReason('');
      await loadBlockedHashes();
    } catch (err) {
      setSnackbar({
        open: true,
        message: err instanceof Error ? err.message : 'Failed to add blocked hash',
        severity: 'error',
      });
    }
  };

  const handleDeleteHash = async () => {
    if (!selectedHash) return;

    try {
      await removeBlockedHash(selectedHash);
      setSnackbar({
        open: true,
        message: 'Hash unblocked successfully',
        severity: 'success',
      });
      setDeleteDialogOpen(false);
      setSelectedHash(null);
      await loadBlockedHashes();
    } catch (err) {
      setSnackbar({
        open: true,
        message: err instanceof Error ? err.message : 'Failed to remove blocked hash',
        severity: 'error',
      });
    }
  };

  const openDeleteDialog = (hash: string) => {
    setSelectedHash(hash);
    setDeleteDialogOpen(true);
  };

  const formatDate = (dateString: string) => {
    return new Date(dateString).toLocaleString();
  };

  const truncateHash = (hash: string) => {
    return `${hash.slice(0, 8)}...${hash.slice(-8)}`;
  };

  return (
    <Box sx={{ p: 3 }}>
      <Box
        sx={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          mb: 3,
        }}
      >
        <Box>
          <Typography variant="h6" gutterBottom>
            <BlockIcon sx={{ verticalAlign: 'middle', mr: 1 }} />
            Blocked File Hashes
          </Typography>
          <Typography variant="body2" color="text.secondary">
            Files with these hashes will be skipped during import to prevent reimporting deleted or
            unwanted audiobooks.
          </Typography>
        </Box>
        <Button variant="contained" startIcon={<AddIcon />} onClick={() => setAddDialogOpen(true)}>
          Block Hash
        </Button>
      </Box>

      {error && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}

      {loading ? (
        <Typography>Loading...</Typography>
      ) : hashes.length === 0 ? (
        <Paper sx={{ p: 3, textAlign: 'center' }}>
          <BlockIcon sx={{ fontSize: 48, color: 'text.secondary', mb: 2 }} />
          <Typography variant="h6" color="text.secondary" gutterBottom>
            No Blocked Hashes
          </Typography>
          <Typography variant="body2" color="text.secondary" paragraph>
            When you delete an audiobook and choose to prevent reimporting, its file hash will
            appear here.
          </Typography>
          <Button variant="outlined" startIcon={<AddIcon />} onClick={() => setAddDialogOpen(true)}>
            Add Blocked Hash
          </Button>
        </Paper>
      ) : (
        <TableContainer component={Paper}>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Hash</TableCell>
                <TableCell>Reason</TableCell>
                <TableCell>Blocked Date</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {hashes.map((hash) => (
                <TableRow key={hash.hash}>
                  <TableCell>
                    <Typography variant="body2" fontFamily="monospace" title={hash.hash}>
                      {truncateHash(hash.hash)}
                    </Typography>
                  </TableCell>
                  <TableCell>{hash.reason}</TableCell>
                  <TableCell>{formatDate(hash.created_at)}</TableCell>
                  <TableCell align="right">
                    <IconButton
                      size="small"
                      color="error"
                      onClick={() => openDeleteDialog(hash.hash)}
                      title="Unblock this hash"
                    >
                      <DeleteIcon />
                    </IconButton>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {/* Add Hash Dialog */}
      <Dialog open={addDialogOpen} onClose={() => setAddDialogOpen(false)} maxWidth="md" fullWidth>
        <DialogTitle>Block File Hash</DialogTitle>
        <DialogContent>
          <Typography variant="body2" color="text.secondary" paragraph>
            Enter the SHA256 hash of a file you want to prevent from being imported. This is
            typically used for files you've deleted and don't want to reimport.
          </Typography>
          <TextField
            label="File Hash (SHA256)"
            value={newHash}
            onChange={(e) => setNewHash(e.target.value.toLowerCase())}
            fullWidth
            margin="normal"
            placeholder="e.g., a3f5b2c1d4e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0f1a2"
            helperText="64 hexadecimal characters"
            inputProps={{
              pattern: '[a-fA-F0-9]{64}',
              maxLength: 64,
              style: { fontFamily: 'monospace' },
            }}
          />
          <TextField
            label="Reason"
            value={newReason}
            onChange={(e) => setNewReason(e.target.value)}
            fullWidth
            margin="normal"
            multiline
            rows={3}
            placeholder="e.g., Low quality version - deleted"
            helperText="Why are you blocking this file?"
          />
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setAddDialogOpen(false)}>Cancel</Button>
          <Button onClick={handleAddHash} variant="contained" color="primary">
            Block Hash
          </Button>
        </DialogActions>
      </Dialog>

      {/* Delete Confirmation Dialog */}
      <Dialog open={deleteDialogOpen} onClose={() => setDeleteDialogOpen(false)}>
        <DialogTitle>Unblock Hash?</DialogTitle>
        <DialogContent>
          <Typography>
            Are you sure you want to unblock this hash? Files with this hash will be able to be
            imported again.
          </Typography>
          {selectedHash && (
            <Typography
              variant="body2"
              fontFamily="monospace"
              sx={{ mt: 2, p: 1, bgcolor: 'grey.100', borderRadius: 1 }}
            >
              {selectedHash}
            </Typography>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setDeleteDialogOpen(false)}>Cancel</Button>
          <Button onClick={handleDeleteHash} variant="contained" color="error">
            Unblock
          </Button>
        </DialogActions>
      </Dialog>

      {/* Snackbar for notifications */}
      <Snackbar
        open={snackbar.open}
        autoHideDuration={6000}
        onClose={() => setSnackbar({ ...snackbar, open: false })}
      >
        <Alert
          severity={snackbar.severity}
          onClose={() => setSnackbar({ ...snackbar, open: false })}
        >
          {snackbar.message}
        </Alert>
      </Snackbar>
    </Box>
  );
}
