// file: web/src/components/settings/MetadataSettingsTab.tsx
// version: 1.6.0
// guid: 9e0f1a2b-3c4d-5e6f-7a8b-9c0d1e2f3a4b
// last-edited: 2026-11-19
import { Dispatch, SetStateAction } from 'react';
import type { SecretStatus } from '../../services/configView';
import { canClearSecret, describeSecretStatus } from '../../utils/secretStatus';
import { describeLock } from '../../utils/settingLocks';
import { hasSavedCredential, type UiMetadataSource } from '../../utils/metadataSources';
import {
  Box,
  Typography,
  TextField,
  Button,
  Grid,
  Alert,
  Divider,
  Paper,
  FormControlLabel,
  Switch,
  Collapse,
  IconButton,
} from '@mui/material';
import {
  DragHandle as DragHandleIcon,
  CheckBox as CheckBoxIcon,
  CheckBoxOutlineBlank as CheckBoxOutlineBlankIcon,
  ExpandMore as ExpandMoreIcon,
  Settings as SettingsIcon,
} from '@mui/icons-material';
import * as api from '../../services/api';
import { OpenLibraryDumps } from './OpenLibraryDumps';

interface MetadataSettingsTabProps {
  settings: {
    enableAIParsing: boolean;
    openaiApiKey: string;
    metadataSources: UiMetadataSource[];
  };
  setSettings: Dispatch<SetStateAction<any>>;
  handleChange: (field: string, value: string | boolean | number | string[]) => void;
  expandedSource: string | null;
  setExpandedSource: (value: string | null) => void;
  openaiTestState: {
    status: 'idle' | 'loading' | 'success' | 'error';
    message?: string;
  };
  openaiKeyError: string | null;
  /** What the secret store says about the OpenAI key: set or not, never the key. */
  openaiKeyStatus: SecretStatus | undefined;
  /** What locks the OpenAI key (`setting_locks.openai_api_key`), when something does. */
  openaiKeyLock?: string;
  handleClearOpenAIKey: () => void;
  sourceTestStatus: Record<
    string,
    { testing: boolean; result?: { success: boolean; message?: string; error?: string } }
  >;
  handleTestAIConnection: () => void;
  handleSourceToggle: (sourceId: string) => void;
  handleTestMetadataSource: (sourceId: string) => void;
  handleCredentialChange: (sourceId: string, field: string, value: string) => void;
  handleToggleClearCredential: (sourceId: string, field: string) => void;
  handleSourceReorder: (sourceId: string, direction: 'up' | 'down') => void;
}

export function MetadataSettingsTab(props: MetadataSettingsTabProps) {
  const {
    settings,
    handleChange,
    expandedSource,
    setExpandedSource,
    openaiTestState,
    openaiKeyError,
    openaiKeyStatus,
    openaiKeyLock,
    handleClearOpenAIKey,
    sourceTestStatus,
    handleTestAIConnection,
    handleSourceToggle,
    handleTestMetadataSource,
    handleCredentialChange,
    handleToggleClearCredential,
    handleSourceReorder,
  } = props;

  const openaiPlaceholder = openaiKeyStatus?.set
    ? 'Saved (type a new key to replace it)'
    : 'Enter your OpenAI API key';

  const openaiHelperText =
    openaiKeyError ||
    (openaiKeyLock
      ? `Locked: set by ${describeLock(openaiKeyLock)}, which overrides any saved key.`
      : settings.enableAIParsing
        ? describeSecretStatus(openaiKeyStatus, 'Required for AI parsing')
        : '');

  return (
    <Grid container spacing={3}>
      <Grid size={12}>
        <Typography variant="h6" gutterBottom>
          Metadata Settings
        </Typography>
        <Divider sx={{ mb: 2 }} />
      </Grid>

      {/* AI-Powered Parsing Section */}
      <Grid size={12}>
        <Typography variant="subtitle1" gutterBottom sx={{ mt: 2 }}>
          AI-Powered Filename Parsing
        </Typography>
        <Divider sx={{ mb: 2 }} />
      </Grid>

      <Grid size={12}>
        <FormControlLabel
          control={
            <Switch
              checked={settings.enableAIParsing}
              onChange={(e) => handleChange('enableAIParsing', e.target.checked)}
            />
          }
          label="Enable AI-powered filename parsing"
        />
        <Alert severity="info" sx={{ mt: 1, mb: 2 }}>
          <Typography variant="caption">
            <strong>What is this?</strong> Uses OpenAI to intelligently parse complex audiobook
            filenames into title, author, series, narrator, etc. This dramatically improves metadata
            extraction from poorly named files where traditional parsing fails.
          </Typography>
        </Alert>
      </Grid>

      <Grid size={12}>
        <Box
          sx={{
            display: 'flex',
            flexDirection: { xs: 'column', md: 'row' },
            gap: 2,
            alignItems: { xs: 'stretch', md: 'center' },
          }}
        >
          <TextField
            fullWidth
            label="OpenAI API Key"
            type="password"
            value={settings.openaiApiKey}
            onChange={(e) => handleChange('openaiApiKey', e.target.value)}
            autoComplete="off"
            disabled={!settings.enableAIParsing || !!openaiKeyLock}
            error={Boolean(openaiKeyError)}
            placeholder={openaiPlaceholder}
            helperText={openaiHelperText}
          />
          {canClearSecret(openaiKeyStatus) && !openaiKeyLock && (
            <Button variant="text" color="error" onClick={handleClearOpenAIKey}>
              Clear key
            </Button>
          )}
          <Button
            variant="outlined"
            onClick={handleTestAIConnection}
            disabled={!settings.enableAIParsing || openaiTestState.status === 'loading'}
          >
            {openaiTestState.status === 'loading' ? 'Testing...' : 'Test Connection'}
          </Button>
        </Box>
        {openaiTestState.status === 'success' && (
          <Alert severity="success" sx={{ mt: 2 }}>
            {openaiTestState.message}
          </Alert>
        )}
        {openaiTestState.status === 'error' && (
          <Alert severity="error" sx={{ mt: 2 }}>
            {openaiTestState.message}
          </Alert>
        )}
      </Grid>

      <Grid size={12}>
        <Typography variant="subtitle1" gutterBottom>
          Metadata Sources (Priority Order)
        </Typography>
        <Alert severity="info" sx={{ mb: 2 }}>
          <Typography variant="caption">
            Sources are queried in order. If a field is missing from the first source, the system
            automatically falls back to the next enabled source to fill in additional fields.
          </Typography>
        </Alert>
        <Paper variant="outlined" sx={{ p: 2 }}>
          {settings.metadataSources.map((source, index) => (
            <Box key={source.id}>
              <Box
                sx={{
                  display: 'flex',
                  alignItems: 'center',
                  py: 1.5,
                  px: 1,
                  bgcolor: source.enabled ? 'transparent' : 'action.disabledBackground',
                }}
              >
                <Box sx={{ display: 'flex', flexDirection: 'column', mr: 1 }}>
                  <Button
                    size="small"
                    onClick={() => handleSourceReorder(source.id, 'up')}
                    disabled={index === 0}
                    sx={{ minWidth: 'auto', p: 0.5 }}
                  >
                    ▲
                  </Button>
                  <Button
                    size="small"
                    onClick={() => handleSourceReorder(source.id, 'down')}
                    disabled={index === settings.metadataSources.length - 1}
                    sx={{ minWidth: 'auto', p: 0.5 }}
                  >
                    ▼
                  </Button>
                </Box>
                <DragHandleIcon sx={{ mr: 2, color: 'text.disabled' }} />
                <Box sx={{ display: 'flex', alignItems: 'center', flex: 1 }}>
                  <Typography
                    variant="body1"
                    sx={{
                      fontWeight: source.enabled ? 500 : 400,
                      color: source.enabled ? 'text.primary' : 'text.disabled',
                    }}
                  >
                    {source.priority}. {source.name}
                  </Typography>
                  {source.requiresAuth && (
                    <Typography variant="caption" sx={{ ml: 1, color: 'text.secondary' }}>
                      (requires API key)
                    </Typography>
                  )}
                </Box>
                {source.requiresAuth && (
                  <IconButton
                    size="small"
                    onClick={() =>
                      setExpandedSource(expandedSource === source.id ? null : source.id)
                    }
                    sx={{ mr: 1 }}
                  >
                    <ExpandMoreIcon
                      sx={[
                        {
                          transition: 'transform 0.3s',
                        },
                        expandedSource === source.id
                          ? {
                              transform: 'rotate(180deg)',
                            }
                          : {
                              transform: 'rotate(0deg)',
                            },
                      ]}
                    />
                  </IconButton>
                )}
                <Button
                  size="small"
                  onClick={() => handleSourceToggle(source.id)}
                  startIcon={source.enabled ? <CheckBoxIcon /> : <CheckBoxOutlineBlankIcon />}
                >
                  {source.enabled ? 'Enabled' : 'Disabled'}
                </Button>
              </Box>
              {source.requiresAuth && (
                <Collapse in={expandedSource === source.id}>
                  <Box
                    sx={{
                      p: 2,
                      bgcolor: 'background.default',
                      borderTop: 1,
                      borderColor: 'divider',
                    }}
                  >
                    <Typography
                      variant="subtitle2"
                      gutterBottom
                      sx={{
                        display: 'flex',
                        alignItems: 'center',
                        mb: 2,
                      }}
                    >
                      <SettingsIcon sx={{ mr: 1, fontSize: 18 }} />
                      API Configuration
                    </Typography>
                    <Grid container spacing={2}>
                      <Grid size={12}>
                        <TextField
                          fullWidth
                          size="small"
                          label={`${source.name} API Key`}
                          type="password"
                          value={source.credentials.apiKey || ''}
                          onChange={(e) =>
                            handleCredentialChange(source.id, 'apiKey', e.target.value)
                          }
                          autoComplete="off"
                          placeholder={
                            hasSavedCredential(source, 'apiKey')
                              ? 'Saved (type a new key to replace it)'
                              : 'Enter your ' + source.name + ' API key'
                          }
                          helperText={
                            source.clearCredentials.includes('apiKey')
                              ? 'The saved key will be deleted when you save.'
                              : hasSavedCredential(source, 'apiKey')
                                ? 'A key is saved. Type a new one to replace it.'
                                : undefined
                          }
                        />
                      </Grid>
                      <Grid size={12}>
                        {source.savedCredentials.includes('apiKey') && (
                          <Button
                            size="small"
                            variant="text"
                            color={source.clearCredentials.includes('apiKey') ? 'inherit' : 'error'}
                            sx={{ mr: 1 }}
                            onClick={() => handleToggleClearCredential(source.id, 'apiKey')}
                          >
                            {source.clearCredentials.includes('apiKey')
                              ? 'Keep saved key'
                              : 'Clear saved key'}
                          </Button>
                        )}
                        <Button
                          size="small"
                          variant="outlined"
                          disabled={
                            !source.credentials.apiKey || sourceTestStatus[source.id]?.testing
                          }
                          onClick={() => handleTestMetadataSource(source.id)}
                        >
                          {sourceTestStatus[source.id]?.testing ? 'Testing...' : 'Test Connection'}
                        </Button>
                        {sourceTestStatus[source.id]?.result && (
                          <Typography
                            variant="caption"
                            sx={{
                              ml: 2,
                              color: sourceTestStatus[source.id]?.result?.success
                                ? 'success.main'
                                : 'error.main',
                            }}
                          >
                            {sourceTestStatus[source.id]?.result?.success
                              ? sourceTestStatus[source.id]?.result?.message
                              : sourceTestStatus[source.id]?.result?.error}
                          </Typography>
                        )}
                      </Grid>
                      {source.id === 'google-books' && (
                        <Grid size={12}>
                          <Typography
                            variant="caption"
                            component="div"
                            sx={{
                              color: 'text.secondary',
                            }}
                          >
                            Setup (2 clicks): <strong>1.</strong>{' '}
                            <a
                              href={
                                'https://console.cloud.google.com/' +
                                'flows/enableapi?' +
                                'apiid=books.googleapis.com'
                              }
                              target="_blank"
                              rel="noopener noreferrer"
                            >
                              Enable Books API
                            </a>{' '}
                            <strong>2.</strong>{' '}
                            <a
                              href={
                                'https://console.cloud.google.com/' +
                                'apis/credentials/wizard?' +
                                'api=books.googleapis.com'
                              }
                              target="_blank"
                              rel="noopener noreferrer"
                            >
                              Create API Key
                            </a>{' '}
                            then paste it above. Free tier: 1,000 requests/day.
                          </Typography>
                        </Grid>
                      )}
                      {source.id === 'hardcover' && (
                        <Grid size={12}>
                          <Typography
                            variant="caption"
                            component="div"
                            sx={{
                              color: 'text.secondary',
                            }}
                          >
                            Get your API key from{' '}
                            <a
                              href="https://hardcover.app/account/api"
                              target="_blank"
                              rel="noopener noreferrer"
                            >
                              hardcover.app/account/api
                            </a>{' '}
                            (free account required).
                          </Typography>
                        </Grid>
                      )}
                    </Grid>
                  </Box>
                </Collapse>
              )}
              {index < settings.metadataSources.length - 1 && <Divider />}
            </Box>
          ))}
        </Paper>
      </Grid>

      {/* Open Library Data Dumps Section */}
      <Grid size={12}>
        <Typography variant="subtitle1" gutterBottom sx={{ mt: 2 }}>
          Open Library Data Dumps
        </Typography>
        <Divider sx={{ mb: 2 }} />
        <Typography
          variant="body2"
          sx={{
            color: 'text.secondary',
            mb: 2,
          }}
        >
          Download Open Library data dumps for fast, offline metadata lookups. Dumps are ~12GB total
          and enable near-instant ISBN and title searches without API rate limits.
        </Typography>
      </Grid>

      <Grid size={12}>
        <OpenLibraryDumps />
      </Grid>

      {/* Database Maintenance */}
      <Grid size={12}>
        <Typography variant="subtitle1" gutterBottom sx={{ mt: 2 }}>
          Database Maintenance
        </Typography>
        <Divider sx={{ mb: 2 }} />
        <Typography
          variant="body2"
          sx={{
            color: 'text.secondary',
            mb: 2,
          }}
        >
          Split compound author/narrator names (e.g. &quot;Author A &amp; Author B&quot;) into
          separate records for better matching and display.
        </Typography>
        <Button
          variant="outlined"
          onClick={async () => {
            try {
              const result = await api.optimizeDatabase();
              alert(
                `Processed ${result.books_processed} books.\n` +
                  `Authors split: ${result.authors_split}\n` +
                  `Narrators split: ${result.narrators_split}`
              );
            } catch {
              alert('Failed to optimize database');
            }
          }}
        >
          Optimize Database
        </Button>
      </Grid>
    </Grid>
  );
}
