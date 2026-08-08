// file: web/src/components/layout/UserMenu.tsx
// version: 1.0.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890

import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Avatar,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Divider,
  ListItemIcon,
  Menu,
  MenuItem,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import AccountCircleIcon from '@mui/icons-material/AccountCircle';
import LockIcon from '@mui/icons-material/Lock';
import LogoutIcon from '@mui/icons-material/Logout';
import PersonIcon from '@mui/icons-material/Person';
import { useAuth } from '../../contexts/AuthContext';
import * as api from '../../services/api';

type DialogMode = 'profile' | 'password' | null;

export function UserMenu() {
  const navigate = useNavigate();
  const auth = useAuth();
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null);
  const [dialogMode, setDialogMode] = useState<DialogMode>(null);

  // Profile form state
  const [email, setEmail] = useState('');
  const [profileError, setProfileError] = useState('');
  const [profileSaving, setProfileSaving] = useState(false);

  // Password form state
  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [passwordError, setPasswordError] = useState('');
  const [passwordSaving, setPasswordSaving] = useState(false);

  if (!auth.requiresAuth || !auth.user) return null;

  const username = auth.user.username;
  const initials = username.slice(0, 2).toUpperCase();

  const openMenu = (e: React.MouseEvent<HTMLElement>) => setAnchorEl(e.currentTarget);
  const closeMenu = () => setAnchorEl(null);

  const openDialog = (mode: DialogMode) => {
    closeMenu();
    if (mode === 'profile') {
      setEmail(auth.user?.email ?? '');
      setProfileError('');
    }
    if (mode === 'password') {
      setCurrentPassword('');
      setNewPassword('');
      setConfirmPassword('');
      setPasswordError('');
    }
    setDialogMode(mode);
  };

  const closeDialog = () => setDialogMode(null);

  const handleLogout = async () => {
    closeMenu();
    try {
      await auth.logout();
    } finally {
      navigate('/login');
    }
  };

  const saveProfile = async () => {
    setProfileError('');
    if (!email.trim()) {
      setProfileError('Email is required');
      return;
    }
    setProfileSaving(true);
    try {
      const updated = await api.updateMe({ email: email.trim() });
      await auth.refresh();
      closeDialog();
      // If auth.refresh doesn't update the displayed user fast enough, the
      // updated value is available via updated.email — but refresh() re-fetches
      // from the server so the context will be consistent.
      void updated;
    } catch (e: unknown) {
      setProfileError(String(e));
    } finally {
      setProfileSaving(false);
    }
  };

  const savePassword = async () => {
    setPasswordError('');
    if (newPassword.length < 8) {
      setPasswordError('New password must be at least 8 characters');
      return;
    }
    if (newPassword !== confirmPassword) {
      setPasswordError('Passwords do not match');
      return;
    }
    setPasswordSaving(true);
    try {
      await api.changePassword({
        current_password: currentPassword,
        new_password: newPassword,
      });
      closeDialog();
    } catch (e: unknown) {
      setPasswordError(String(e));
    } finally {
      setPasswordSaving(false);
    }
  };

  return (
    <>
      <Tooltip title={username}>
        <Chip
          avatar={<Avatar sx={{ bgcolor: 'primary.dark' }}>{initials}</Avatar>}
          label={username}
          onClick={openMenu}
          sx={{
            color: 'inherit',
            borderColor: 'rgba(255,255,255,0.4)',
            cursor: 'pointer',
            '&:hover': { borderColor: 'rgba(255,255,255,0.8)' },
          }}
          variant="outlined"
        />
      </Tooltip>

      <Menu
        anchorEl={anchorEl}
        open={Boolean(anchorEl)}
        onClose={closeMenu}
        transformOrigin={{ horizontal: 'right', vertical: 'top' }}
        anchorOrigin={{ horizontal: 'right', vertical: 'bottom' }}
        slotProps={{ paper: { sx: { minWidth: 200, mt: 0.5 } } }}
      >
        <Box sx={{ px: 2, py: 1 }}>
          <Typography variant="subtitle2">{username}</Typography>
          <Typography variant="caption" color="text.secondary">
            {auth.user.email || 'No email set'}
          </Typography>
        </Box>
        <Divider />
        <MenuItem onClick={() => openDialog('profile')}>
          <ListItemIcon><PersonIcon fontSize="small" /></ListItemIcon>
          Edit profile
        </MenuItem>
        <MenuItem onClick={() => openDialog('password')}>
          <ListItemIcon><LockIcon fontSize="small" /></ListItemIcon>
          Change password
        </MenuItem>
        <Divider />
        <MenuItem onClick={() => { void handleLogout(); }}>
          <ListItemIcon><LogoutIcon fontSize="small" /></ListItemIcon>
          Logout
        </MenuItem>
      </Menu>

      {/* Profile dialog */}
      <Dialog open={dialogMode === 'profile'} onClose={closeDialog} maxWidth="xs" fullWidth>
        <DialogTitle>
          <Stack direction="row" alignItems="center" spacing={1}>
            <AccountCircleIcon />
            <span>Edit profile</span>
          </Stack>
        </DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ mt: 1 }}>
            <TextField
              label="Username"
              value={username}
              disabled
              fullWidth
              size="small"
            />
            <TextField
              label="Email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              fullWidth
              size="small"
              type="email"
              autoFocus
              onKeyDown={(e) => { if (e.key === 'Enter') void saveProfile(); }}
            />
            {profileError && (
              <Typography variant="body2" color="error">{profileError}</Typography>
            )}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={closeDialog}>Cancel</Button>
          <Button variant="contained" onClick={() => void saveProfile()} disabled={profileSaving}>
            {profileSaving ? 'Saving…' : 'Save'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* Change password dialog */}
      <Dialog open={dialogMode === 'password'} onClose={closeDialog} maxWidth="xs" fullWidth>
        <DialogTitle>
          <Stack direction="row" alignItems="center" spacing={1}>
            <LockIcon />
            <span>Change password</span>
          </Stack>
        </DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ mt: 1 }}>
            <TextField
              label="Current password"
              value={currentPassword}
              onChange={(e) => setCurrentPassword(e.target.value)}
              fullWidth
              size="small"
              type="password"
              autoFocus
            />
            <TextField
              label="New password"
              value={newPassword}
              onChange={(e) => setNewPassword(e.target.value)}
              fullWidth
              size="small"
              type="password"
            />
            <TextField
              label="Confirm new password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              fullWidth
              size="small"
              type="password"
              onKeyDown={(e) => { if (e.key === 'Enter') void savePassword(); }}
            />
            {passwordError && (
              <Typography variant="body2" color="error">{passwordError}</Typography>
            )}
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={closeDialog}>Cancel</Button>
          <Button variant="contained" onClick={() => void savePassword()} disabled={passwordSaving}>
            {passwordSaving ? 'Saving…' : 'Save'}
          </Button>
        </DialogActions>
      </Dialog>
    </>
  );
}
