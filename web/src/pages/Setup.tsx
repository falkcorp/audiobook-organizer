// file: web/src/pages/Setup.tsx
// version: 1.1.1
// guid: 0f8a9b4c-1d2e-4a70-b8c5-3d7e0f1b9a99

import React, { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  Box,
  Button,
  Card,
  CardContent,
  Container,
  TextField,
  Typography,
  Alert,
} from '@mui/material';
const API_BASE = '/api/v1';

export default function Setup() {
  const navigate = useNavigate();
  const [username, setUsername] = useState('admin');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [email, setEmail] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError('');

    if (password.length < 8) {
      setError('Password must be at least 8 characters');
      return;
    }
    if (password !== confirmPassword) {
      setError('Passwords do not match');
      return;
    }

    setLoading(true);
    try {
      const resp = await fetch(`${API_BASE}/auth/setup`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password, email }),
      });
      if (!resp.ok) {
        const body = await resp.json().catch(() => ({}));
        throw Object.assign(new Error(body.error || 'Setup failed'), { response: { data: body } });
      }
      navigate('/');
    } catch (err: unknown) {
      const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
      setError(msg || 'Setup failed');
    } finally {
      setLoading(false);
    }
  };

  return (
    <Container maxWidth="sm" sx={{ mt: 8 }}>
      <Card>
        <CardContent>
          <Typography variant="h4" gutterBottom>
            Welcome to Audiobook Organizer
          </Typography>
          <Typography
            variant="body1"
            sx={{
              color: 'text.secondary',
              mb: 3,
            }}
          >
            Create your admin account to get started.
          </Typography>

          {error && (
            <Alert severity="error" sx={{ mb: 2 }}>
              {error}
            </Alert>
          )}

          <Box
            component="form"
            id="setup-form"
            name="setup-form"
            method="post"
            action="#"
            onSubmit={handleSubmit}
          >
            <TextField
              fullWidth
              label="Username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              margin="normal"
              name="username"
              id="username"
              type="text"
              autoComplete="username"
              required
              autoFocus
              slotProps={{
                htmlInput: {
                  autoCapitalize: 'none',
                  autoCorrect: 'off',
                  spellCheck: false,
                },
              }}
            />
            <TextField
              fullWidth
              label="Email (optional)"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              margin="normal"
              name="email"
              id="email"
              type="email"
              autoComplete="email"
              slotProps={{
                htmlInput: {
                  autoCapitalize: 'none',
                  autoCorrect: 'off',
                  spellCheck: false,
                },
              }}
            />
            <TextField
              fullWidth
              label="Password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              margin="normal"
              name="password"
              id="password"
              type="password"
              autoComplete="new-password"
              required
              helperText="Minimum 8 characters"
            />
            <TextField
              fullWidth
              label="Confirm Password"
              value={confirmPassword}
              onChange={(e) => setConfirmPassword(e.target.value)}
              margin="normal"
              name="confirm-password"
              id="confirm-password"
              type="password"
              autoComplete="new-password"
              required
            />
            <Button
              type="submit"
              variant="contained"
              fullWidth
              size="large"
              disabled={loading}
              sx={{ mt: 2 }}
            >
              {loading ? 'Creating...' : 'Create Admin Account'}
            </Button>
          </Box>
        </CardContent>
      </Card>
    </Container>
  );
}
