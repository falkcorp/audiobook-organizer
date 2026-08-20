// file: web/src/components/settings/ITunesConflictDialog.tsx
// version: 1.0.1
// guid: g2f3a4b5-c6d7-8901-ghij-k1l2m3n4o5p6

import React from 'react';
import {
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  Button,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Radio,
  RadioGroup,
  FormControlLabel,
  Stack,
  Typography,
  Paper,
} from '@mui/material';

export interface ConflictItem {
  bookId: string;
  bookTitle: string;
  fieldName: string;
  itunesVersion: string;
  organizerVersion: string;
  itunesModified: string;
  organizerModified: string;
}

export interface ITunesConflictDialogProps {
  open: boolean;
  conflicts: ConflictItem[];
  loading?: boolean;
  onResolve: (resolutions: Record<string, 'itunes' | 'organizer'>) => void;
  onCancel: () => void;
}

/**
 * ITunesConflictDialog displays conflicts between iTunes and organizer data
 * and allows user to select which version to keep for each conflict
 */
export function ITunesConflictDialog({
  open,
  conflicts,
  loading = false,
  onResolve,
  onCancel,
}: ITunesConflictDialogProps) {
  const [resolutions, setResolutions] = React.useState<Record<string, 'itunes' | 'organizer'>>({});

  React.useEffect(() => {
    // Initialize resolutions with default (prefer iTunes for first-time imports)
    const initial: Record<string, 'itunes' | 'organizer'> = {};
    for (const conflict of conflicts) {
      const key = `${conflict.bookId}-${conflict.fieldName}`;
      initial[key] = 'itunes'; // Default to iTunes version
    }
    setResolutions(initial);
  }, [conflicts]);

  const handleResolve = (conflictId: string, choice: 'itunes' | 'organizer') => {
    setResolutions((prev) => ({
      ...prev,
      [conflictId]: choice,
    }));
  };

  const handleBulkResolve = (choice: 'itunes' | 'organizer') => {
    const bulk: Record<string, 'itunes' | 'organizer'> = {};
    for (const conflict of conflicts) {
      const key = `${conflict.bookId}-${conflict.fieldName}`;
      bulk[key] = choice;
    }
    setResolutions(bulk);
  };

  const handleApply = () => {
    onResolve(resolutions);
  };

  return (
    <Dialog open={open} maxWidth="lg" fullWidth>
      <DialogTitle>Sync Conflicts Detected ({conflicts.length} conflicts)</DialogTitle>
      <DialogContent>
        <Stack spacing={2} sx={{ mt: 1 }}>
          <Typography variant="body2" color="textSecondary">
            Review conflicts and choose which version to keep for each field
          </Typography>

          <Stack direction="row" spacing={1}>
            <Button size="small" variant="outlined" onClick={() => handleBulkResolve('itunes')}>
              Use iTunes for all
            </Button>
            <Button size="small" variant="outlined" onClick={() => handleBulkResolve('organizer')}>
              Use Organizer for all
            </Button>
          </Stack>

          <TableContainer component={Paper}>
            <Table size="small">
              <TableHead>
                <TableRow sx={{ backgroundColor: '#f5f5f5' }}>
                  <TableCell width="25%">Book</TableCell>
                  <TableCell width="15%">Field</TableCell>
                  <TableCell width="20%">iTunes Version</TableCell>
                  <TableCell width="20%">Organizer Version</TableCell>
                  <TableCell width="20%" align="center">
                    Choice
                  </TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {conflicts.map((conflict) => {
                  const key = `${conflict.bookId}-${conflict.fieldName}`;
                  const choice = resolutions[key] || 'itunes';

                  return (
                    <TableRow key={key}>
                      <TableCell variant="head">{conflict.bookTitle}</TableCell>
                      <TableCell>{conflict.fieldName}</TableCell>
                      <TableCell>
                        <Typography variant="caption">{conflict.itunesVersion}</Typography>
                        <Typography variant="caption" color="textSecondary" display="block">
                          Modified: {conflict.itunesModified}
                        </Typography>
                      </TableCell>
                      <TableCell>
                        <Typography variant="caption">{conflict.organizerVersion}</Typography>
                        <Typography variant="caption" color="textSecondary" display="block">
                          Modified: {conflict.organizerModified}
                        </Typography>
                      </TableCell>
                      <TableCell align="center">
                        <RadioGroup
                          value={choice}
                          onChange={(e) =>
                            handleResolve(key, e.target.value as 'itunes' | 'organizer')
                          }
                          row
                        >
                          <FormControlLabel
                            value="itunes"
                            control={<Radio size="small" />}
                            label="iTunes"
                          />
                          <FormControlLabel
                            value="organizer"
                            control={<Radio size="small" />}
                            label="Org"
                          />
                        </RadioGroup>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </TableContainer>
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onCancel} disabled={loading}>
          Cancel
        </Button>
        <Button onClick={handleApply} variant="contained" disabled={loading}>
          {loading ? 'Syncing...' : 'Apply & Sync'}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
