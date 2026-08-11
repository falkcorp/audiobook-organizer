// file: web/src/components/settings/MetadataSettingsTab.tsx
// version: 1.1.0
// guid: 9e0f1a2b-3c4d-5e6f-7a8b-9c0d1e2f3a4b
// last-edited: 2026-08-10
import { Dispatch, SetStateAction } from 'react';
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

interface UiMetadataSource {
  id: string;
  name: string;
  enabled: boolean;
  priority: number;
  requiresAuth: boolean;
  credentials: {
    apiKey?: string;
  };
}

interface MetadataSettingsTabProps {
  settings: {
    autoFetchMetadata: boolean;
    enableAIParsing: boolean;
    metadataLLMScoringEnabled: boolean;
    openaiApiKey: string;
    metadataSources: UiMetadataSource[];
    language: string;
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
  savedApiKeyMask: string;
  setSavedApiKeyMask: (value: string) => void;
  sourceTestStatus: Record<string, { testing: boolean; result?: { success: boolean; message?: string; error?: string } }>;
  handleTestAIConnection: () => void;
  handleSourceToggle: (sourceId: string) => void;
  handleTestMetadataSource: (sourceId: string) => void;
  handleCredentialChange: (sourceId: string, field: string, value: string) => void;
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
    savedApiKeyMask,
    setSavedApiKeyMask,
    sourceTestStatus,
    handleTestAIConnection,
    handleSourceToggle,
    handleTestMetadataSource,
    handleCredentialChange,
    handleSourceReorder,
  } = props;

  const openaiPlaceholder = savedApiKeyMask
    ? `Key saved: ${savedApiKeyMask} (enter new key to change)`
    : 'Enter your OpenAI API key';

  const openaiHelperText =
    openaiKeyError ||
    (settings.enableAIParsing
      ? savedApiKeyMask
        ? 'Key configured'
        : 'Required for AI parsing'
      : '');

  return (
    <Grid container spacing={3}>
      <Grid item xs={12}>
        <Typography variant="h6" gutterBottom>
          Metadata Settings
        </Typography>
        <Divider sx={{ mb: 2 }} />
      </Grid>

      <Grid item xs={12}>
        <FormControlLabel
          control={
            <Switch
              checked={settings.autoFetchMetadata}
              onChange={(e) =>
                handleChange('autoFetchMetadata', e.target.checked)
              }
            />
          }
          label="Automatically fetch missing metadata"
        />
      </Grid>

      {/* AI-Powered Parsing Section */}
      <Grid item xs={12}>
        <Typography variant="subtitle1" gutterBottom sx={{ mt: 2 }}>
          AI-Powered Filename Parsing
        </Typography>
        <Divider sx={{ mb: 2 }} />
      </Grid>

      <Grid item xs={12}>
        <FormControlLabel
          control={
            <Switch
              checked={settings.enableAIParsing}
              onChange={(e) =>
                handleChange('enableAIParsing', e.target.checked)
              }
            />
          }
          label="Enable AI-powered filename parsing"
        />
        <Alert severity="info" sx={{ mt: 1, mb: 2 }}>
          <Typography variant="caption">
            <strong>What is this?</strong> Uses OpenAI to intelligently
            parse complex audiobook filenames into title, author, series,
            narrator, etc. This dramatically improves metadata extraction
            from poorly named files where traditional parsing fails.
          </Typography>
        </Alert>
      </Grid>

      <Grid item xs={12}>
        <FormControlLabel
          control={
            <Switch
              checked={settings.metadataLLMScoringEnabled}
              onChange={(e) =>
                handleChange('metadataLLMScoringEnabled', e.target.checked)
              }
            />
          }
          label="Enable AI rerank for metadata search (opt-in per search)"
        />
        <Alert severity="info" sx={{ mt: 1, mb: 2 }}>
          <Typography variant="caption">
            <strong>What is this?</strong> Allows users to request a
            higher-quality LLM rerank pass on ambiguous metadata search results.
            The per-search toggle in the search dialog is only effective when
            this server-wide switch is on. Adds approximately $0.003 per search
            when a user opts in.
          </Typography>
        </Alert>
      </Grid>

      <Grid item xs={12}>
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
            onChange={(e) => {
              handleChange('openaiApiKey', e.target.value);
              // Clear saved mask when user starts typing
              if (e.target.value && savedApiKeyMask) {
                setSavedApiKeyMask('');
              }
            }}
            disabled={!settings.enableAIParsing}
            error={Boolean(openaiKeyError)}
            placeholder={openaiPlaceholder}
            helperText={openaiHelperText}
          />
          <Button
            variant="outlined"
            onClick={handleTestAIConnection}
            disabled={
              !settings.enableAIParsing ||
              openaiTestState.status === 'loading'
            }
          >
            {openaiTestState.status === 'loading'
              ? 'Testing...'
              : 'Test Connection'}
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

      <Grid item xs={12}>
        <Typography variant="subtitle1" gutterBottom>
          Metadata Sources (Priority Order)
        </Typography>
        <Alert severity="info" sx={{ mb: 2 }}>
          <Typography variant="caption">
            Sources are queried in order. If a field is missing from the
            first source, the system automatically falls back to the next
            enabled source to fill in additional fields.
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
                  bgcolor: source.enabled
                    ? 'transparent'
                    : 'action.disabledBackground',
                }}
              >
                <Box
                  sx={{ display: 'flex', flexDirection: 'column', mr: 1 }}
                >
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
                    disabled={
                      index === settings.metadataSources.length - 1
                    }
                    sx={{ minWidth: 'auto', p: 0.5 }}
                  >
                    ▼
                  </Button>
                </Box>
                <DragHandleIcon sx={{ mr: 2, color: 'text.disabled' }} />
                <Box
                  sx={{ display: 'flex', alignItems: 'center', flex: 1 }}
                >
                  <Typography
                    variant="body1"
                    sx={{
                      fontWeight: source.enabled ? 500 : 400,
                      color: source.enabled
                        ? 'text.primary'
                        : 'text.disabled',
                    }}
                  >
                    {source.priority}. {source.name}
                  </Typography>
                  {source.requiresAuth && (
                    <Typography
                      variant="caption"
                      sx={{ ml: 1, color: 'text.secondary' }}
                    >
                      (requires API key)
                    </Typography>
                  )}
                </Box>
                {source.requiresAuth && (
                  <IconButton
                    size="small"
                    onClick={() =>
                      setExpandedSource(
                        expandedSource === source.id ? null : source.id
                      )
                    }
                    sx={{ mr: 1 }}
                  >
                    <ExpandMoreIcon
                      sx={[{
                        transition: 'transform 0.3s'
                      }, expandedSource === source.id ? {
                        transform: 'rotate(180deg)'
                      } : {
                        transform: 'rotate(0deg)'
                      }]}
                    />
                  </IconButton>
                )}
                <Button
                  size="small"
                  onClick={() => handleSourceToggle(source.id)}
                  startIcon={
                    source.enabled ? (
                      <CheckBoxIcon />
                    ) : (
                      <CheckBoxOutlineBlankIcon />
                    )
                  }
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
                      <Grid item xs={12}>
                        <TextField
                          fullWidth
                          size="small"
                          label={`${source.name} API Key`}
                          type="password"
                          value={source.credentials.apiKey || ''}
                          onChange={(e) =>
                            handleCredentialChange(
                              source.id,
                              'apiKey',
                              e.target.value
                            )
                          }
                          placeholder={
                            'Enter your ' + source.name + ' API key'
                          }
                        />
                      </Grid>
                      <Grid item xs={12}>
                        <Button
                          size="small"
                          variant="outlined"
                          disabled={!source.credentials.apiKey || sourceTestStatus[source.id]?.testing}
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
                        <Grid item xs={12}>
                          <Typography
                            variant="caption"
                            color="text.secondary"
                            component="div"
                          >
                            Setup (2 clicks):{' '}
                            <strong>1.</strong>{' '}
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
                            </a>
                            {' '}<strong>2.</strong>{' '}
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
                            </a>
                            {' '} then paste it above. Free tier: 1,000 requests/day.
                          </Typography>
                        </Grid>
                      )}
                      {source.id === 'hardcover' && (
                        <Grid item xs={12}>
                          <Typography
                            variant="caption"
                            color="text.secondary"
                            component="div"
                          >
                            Get your API key from{' '}
                            <a
                              href="https://hardcover.app/account/api"
                              target="_blank"
                              rel="noopener noreferrer"
                            >
                              hardcover.app/account/api
                            </a>
                            {' '}(free account required).
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

      <Grid item xs={12} sm={6}>
        <TextField
          fullWidth
          label="Default Language"
          value={settings.language}
          onChange={(e) => handleChange('language', e.target.value)}
          helperText="ISO 639-1 language code (e.g., en, es, fr)"
        />
      </Grid>

      {/* Open Library Data Dumps Section */}
      <Grid item xs={12}>
        <Typography variant="subtitle1" gutterBottom sx={{ mt: 2 }}>
          Open Library Data Dumps
        </Typography>
        <Divider sx={{ mb: 2 }} />
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          Download Open Library data dumps for fast, offline metadata
          lookups. Dumps are ~12GB total and enable near-instant ISBN and
          title searches without API rate limits.
        </Typography>
      </Grid>

      <Grid item xs={12}>
        <OpenLibraryDumps />
      </Grid>

      {/* Database Maintenance */}
      <Grid item xs={12}>
        <Typography variant="subtitle1" gutterBottom sx={{ mt: 2 }}>
          Database Maintenance
        </Typography>
        <Divider sx={{ mb: 2 }} />
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          Split compound author/narrator names (e.g. &quot;Author A &amp; Author B&quot;)
          into separate records for better matching and display.
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
