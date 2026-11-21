// file: web/src/components/users/UserFootprintDrawer.tsx
// version: 0.1.0
// guid: c3baed69-a6a8-473d-8218-54e9cce70daa
// last-edited: 2026-11-21

// Shell half of the Users page (F9-04 slice 4): one account's linked sign-ins and how much data it holds, opened by ?footprint=<userId>.

import { useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Drawer,
  Table,
  TableBody,
  TableCell,
  TableRow,
  Typography,
} from '@mui/material';
import {
  getUserFootprint,
  type UserFootprint,
} from '../../services/api';
import { IdentityList } from './IdentityList';
import { errorText, familyLabel, type MergeableUser } from './userAccountParts';

/**
 * Read-only drawer: one account's linked sign-ins and how much data it holds.
 * The page keys it by account, so each account starts from an empty state.
 */
export function UserFootprintDrawer({
  user,
  onClose,
}: {
  user: MergeableUser | null;
  onClose: () => void;
}) {
  const [footprint, setFootprint] = useState<UserFootprint | null>(null);
  const [error, setError] = useState('');
  const userId = user?.id;

  useEffect(() => {
    if (!userId) return;
    let cancelled = false;
    getUserFootprint(userId)
      .then((fp) => {
        if (!cancelled) setFootprint(fp);
      })
      .catch((err) => {
        if (!cancelled) setError(errorText(err, 'Failed to load sign-ins and data'));
      });
    return () => {
      cancelled = true;
    };
  }, [userId]);

  return (
    <Drawer anchor="right" open={!!user} onClose={onClose}>
      <Box
        sx={{ width: { xs: '100vw', sm: 420 }, p: 3 }}
        role="region"
        aria-label="Sign-ins & data"
      >
        <Typography variant="h6" gutterBottom>
          Sign-ins &amp; data{user ? `: ${user.username}` : ''}
        </Typography>
        {error && <Alert severity="error">{error}</Alert>}
        {!footprint && !error && <CircularProgress size={24} aria-label="Loading" />}
        {footprint && (
          <>
            <Typography variant="subtitle2" sx={{ mt: 1 }}>
              Account
            </Typography>
            <Typography variant="body2">
              {footprint.user.status}
              {footprint.user.email ? ` · ${footprint.user.email}` : ''}
              {footprint.user.has_password ? ' · has a password' : ' · no password'}
              {footprint.user.merged_into_user_id ? ' · merged into another account' : ''}
            </Typography>
            <Typography variant="subtitle2" sx={{ mt: 2 }}>
              Linked sign-ins
            </Typography>
            <IdentityList identities={footprint.identities} />
            <Typography variant="subtitle2" sx={{ mt: 2 }}>
              Data
            </Typography>
            <Table size="small">
              <TableBody>
                {Object.entries(footprint.counts).map(([family, n]) => (
                  <TableRow key={family}>
                    <TableCell>{familyLabel(family)}</TableCell>
                    <TableCell align="right">{n}</TableCell>
                  </TableRow>
                ))}
                <TableRow>
                  <TableCell>Active web sessions</TableCell>
                  <TableCell align="right">{footprint.active_sessions}</TableCell>
                </TableRow>
              </TableBody>
            </Table>
            {footprint.unreadable.length > 0 && (
              <Alert severity="warning" sx={{ mt: 2 }}>
                {footprint.unreadable.length} row(s) could not be read; a merge would be refused
                until they are fixed.
              </Alert>
            )}
          </>
        )}
        <Box sx={{ mt: 3, textAlign: 'right' }}>
          <Button onClick={onClose}>Close</Button>
        </Box>
      </Box>
    </Drawer>
  );
}
