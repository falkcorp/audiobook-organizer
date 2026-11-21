// file: web/src/components/setup/SetupWizardFrame.tsx
// version: 0.1.0
// guid: 13d95335-3ba1-4936-9f11-576767a69816
// last-edited: 2026-11-21

// F9-04 slice 5: the shell half of the first-run wizard (07 §7.11: the first-run frame): the
// dialog, the stepper, and Back / Next / Complete. The steps and what Complete saves belong to the
// modules; this frame imports no books code and nothing from services/api.

import { useState } from 'react';
import {
  Box,
  Button,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Step,
  StepLabel,
  Stepper,
  Typography,
} from '@mui/material';
import { CheckCircle as CheckCircleIcon, Settings as SettingsIcon } from '@mui/icons-material';
import type { SetupStep } from './setupTypes';

export interface SetupWizardFrameProps {
  open: boolean;
  title: string;
  /** Shown as a chip next to the title when set and not "unknown". */
  version?: string;
  steps: readonly SetupStep[];
  /** Whether the way forward from this step is open. */
  canProceed: (stepId: string) => boolean;
  /** Complete Setup: saves everything the steps collected. */
  onFinish: () => void | Promise<void>;
  saving: boolean;
}

export function SetupWizardFrame({
  open,
  title,
  version,
  steps,
  canProceed,
  onFinish,
  saving,
}: SetupWizardFrameProps) {
  const [activeStep, setActiveStep] = useState(0);
  const handleNext = () => setActiveStep((prev) => prev + 1);
  const handleBack = () => setActiveStep((prev) => prev - 1);
  const current = steps[activeStep];
  const Current = current?.Component;

  return (
    <>
      {/* No onClose: the wizard is dismissible only through its own actions.
          MUI 9 removed disableEscapeKeyDown, but with no onClose handler there
          is nothing for Escape to call, so the behaviour is unchanged. */}
      <Dialog open={open} maxWidth="md" fullWidth>
        <DialogTitle>
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
            <SettingsIcon />
            <Typography variant="h6">{title}</Typography>
            {version && version !== 'unknown' && (
              <Chip label={`v${version}`} size="small" color="primary" variant="outlined" />
            )}
          </Box>
        </DialogTitle>

        <DialogContent>
          <Stepper activeStep={activeStep} sx={{ mb: 4 }}>
            {steps.map((step) => (
              <Step key={step.id}>
                <StepLabel>{step.title}</StepLabel>
              </Step>
            ))}
          </Stepper>

          {Current && <Current onNext={handleNext} />}
        </DialogContent>

        <DialogActions>
          <Button onClick={handleBack} disabled={activeStep === 0 || saving}>
            Back
          </Button>
          <Box sx={{ flex: 1 }} />
          {activeStep === steps.length - 1 ? (
            <Button
              variant="contained"
              onClick={onFinish}
              disabled={saving}
              startIcon={saving ? <CircularProgress size={16} /> : <CheckCircleIcon />}
            >
              {saving ? 'Saving...' : 'Complete Setup'}
            </Button>
          ) : (
            <Button
              variant="contained"
              onClick={handleNext}
              disabled={!current || !canProceed(current.id)}
            >
              Next
            </Button>
          )}
        </DialogActions>
      </Dialog>
    </>
  );
}
