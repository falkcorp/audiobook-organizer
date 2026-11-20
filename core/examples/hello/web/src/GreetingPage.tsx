// file: core/examples/hello/web/src/GreetingPage.tsx
// version: 0.1.0
// guid: 2b5d8f73-1e46-4a09-9c27-7a0e3b6d4f18
// last-edited: 2026-11-20

import { useEffect, useRef, useState } from 'react';
import { Box, Button, Stack, TextField, Typography } from '@mui/material';
import { DataState, type DataStatus } from '@mediacore/ui-kit';
import { useApi } from '@mediacore/shell';

interface Greeting {
  message: string;
  seen: number;
}

/** GET /api/hello/v1/greeting?name=: hello's one page (06 §6.12). */
export default function GreetingPage() {
  const api = useApi();
  const [name, setName] = useState('');
  const [status, setStatus] = useState<DataStatus | 'idle'>('idle');
  const [greeting, setGreeting] = useState<Greeting | null>(null);
  const [error, setError] = useState('');
  const inflight = useRef<AbortController | null>(null);
  useEffect(() => () => inflight.current?.abort(), []);

  const greet = async () => {
    inflight.current?.abort();
    const ctl = new AbortController();
    inflight.current = ctl;
    setStatus('loading');
    try {
      const url = `${api.runtime.baseUrl}/api/hello/v1/greeting?name=${encodeURIComponent(name)}`;
      const res = await api.runtime.fetch(url, { signal: ctl.signal, credentials: 'same-origin' });
      const body = await res.json();
      if (!res.ok) throw new Error(body?.detail || body?.title || `HTTP ${res.status}`);
      setGreeting(body as Greeting);
      setStatus('ready');
    } catch (e) {
      if (ctl.signal.aborted) return;
      setError(e instanceof Error ? e.message : String(e));
      setStatus('error');
    }
  };

  return (
    <Box sx={{ p: 3, maxWidth: 520 }}>
      <Typography variant="h5" component="h1" gutterBottom>
        Greeting
      </Typography>
      <Stack direction="row" spacing={1} component="form" onSubmit={(e) => { e.preventDefault(); void greet(); }}>
        <TextField label="Name" size="small" value={name} onChange={(e) => setName(e.target.value)} />
        <Button type="submit" variant="contained" disabled={name === ''}>
          Greet
        </Button>
      </Stack>
      {status !== 'idle' && (
        <DataState status={status} error={error} onRetry={() => void greet()}>
          {greeting && (
            <Typography sx={{ mt: 2 }} data-testid="greeting">
              {greeting.message} (seen {greeting.seen} times)
            </Typography>
          )}
        </DataState>
      )}
    </Box>
  );
}
