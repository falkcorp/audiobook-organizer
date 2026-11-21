// file: web/src/modules/books/pages/ToolsStep.tsx
// version: 0.1.0
// guid: c8444575-b82c-4ec2-8667-04e0ce26195b
// last-edited: 2026-11-21

// F9-04 slice 5: books' first-run setup step "Tools" (07 §7.11 SetupContribution.steps), moved verbatim from the WelcomeWizard.

import {
  Button,
  Typography,
  Box,
  FormControl,
  FormControlLabel,
  Radio,
  RadioGroup,
  Stack,
} from '@mui/material';
import { ToolsPanel } from '../../../components/tools/ToolsPanel';
import { useBooksSetup } from './booksSetupContext';

export function ToolsStep({ onNext }: { onNext: () => void }) {
  const { toolInstallChoice, setToolInstallChoice } = useBooksSetup();
  const skip = () => {
    setToolInstallChoice('skip');
    onNext();
  };
  return (
    <Box>
      <Typography variant="h6" gutterBottom>
        Set up AI &amp; fingerprinting tools
      </Typography>
      <Typography
        variant="body2"
        sx={{
          color: 'text.secondary',
          mb: 2,
        }}
      >
        These tools power local AI deduplication and audio fingerprint matching. Recommended
        installs everything automatically.
      </Typography>

      <FormControl component="fieldset" sx={{ mb: 2 }}>
        <RadioGroup
          value={toolInstallChoice}
          onChange={(e) => setToolInstallChoice(e.target.value as 'recommended' | 'custom')}
        >
          <FormControlLabel
            value="recommended"
            control={<Radio />}
            label={
              <Stack>
                <Typography>Install recommended tools</Typography>
                <Typography
                  variant="caption"
                  sx={{
                    color: 'text.secondary',
                  }}
                >
                  Ollama (local AI, ~5GB) + fpcalc (fingerprinting, ~2MB)
                </Typography>
              </Stack>
            }
          />
          <FormControlLabel value="custom" control={<Radio />} label="Let me choose" />
        </RadioGroup>
      </FormControl>

      {toolInstallChoice === 'custom' && <ToolsPanel mode="wizard" />}

      <Button
        variant="text"
        size="small"
        onClick={skip}
        sx={{ mt: 1 }}
      >
        Skip — I'll set this up later
      </Button>
    </Box>
  );
}
