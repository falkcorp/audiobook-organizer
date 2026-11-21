// file: web/src/modules/books/pages/AiSetupStep.tsx
// version: 0.1.0
// guid: 508dc5d9-c18a-49fe-bf22-7c4ee2d06efb
// last-edited: 2026-11-21

// F9-04 slice 5: books' first-run setup step "AI Setup (Optional)" (07 §7.11 SetupContribution.steps), moved verbatim from the WelcomeWizard.

import {
  Button,
  Typography,
  TextField,
  Box,
  Alert,
  CircularProgress,
} from '@mui/material';
import { useBooksSetup } from './booksSetupContext';

export function AiSetupStep() {
  const { openaiKey, setOpenaiKey, testingKey, keyTestResult, handleTestOpenAIKey } = useBooksSetup();
  return (
    <Box>
      <Typography variant="h6" gutterBottom>
        AI-Powered Metadata (Optional)
      </Typography>
      <Typography
        variant="body2"
        sx={{
          color: 'text.secondary',
          marginBottom: '16px',
        }}
      >
        Enable AI-powered author name parsing and metadata enhancement with OpenAI. This is
        optional and can be configured later in Settings.
      </Typography>

      <TextField
        fullWidth
        label="OpenAI API Key"
        type="password"
        value={openaiKey}
        onChange={(e) => setOpenaiKey(e.target.value)}
        placeholder="sk-..."
        sx={{ mb: 2 }}
        helperText="Get your API key from platform.openai.com"
      />

      <Button
        variant="outlined"
        onClick={handleTestOpenAIKey}
        disabled={!(openaiKey || '').trim() || testingKey}
        startIcon={testingKey ? <CircularProgress size={16} /> : null}
        sx={{ mb: 2 }}
      >
        Test Connection
      </Button>

      {keyTestResult === 'success' && (
        <Alert severity="success" sx={{ mb: 2 }}>
          API key is valid and working!
        </Alert>
      )}

      {keyTestResult === 'error' && (
        <Alert severity="error" sx={{ mb: 2 }}>
          Invalid API key or connection failed. Please check your key.
        </Alert>
      )}

      <Alert severity="info">
        You can skip this step and configure it later in Settings if needed.
      </Alert>
    </Box>
  );
}
