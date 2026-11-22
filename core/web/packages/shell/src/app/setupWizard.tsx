// file: core/web/packages/shell/src/app/setupWizard.tsx
// version: 0.1.0
// guid: e63b0b58-a8b7-4c25-b2f9-0341412ea815
// last-edited: 2026-11-22

import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Alert, Box, Button, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle, Step, StepLabel, Stepper, Typography } from '@mui/material';
import type { ModuleUI, SetupContribution, SetupStepProps } from '../types';
import { useMountedModules, useShellContext } from './context';
import { LazyWithProps } from './LazySlot';

type Setup = SetupContribution<ModuleUI['manifest']>;
type StepEntry = { moduleId: string; id: string; title: string; step: Setup['steps'][number] };

const message = (e: unknown) => (e instanceof Error && e.message ? e.message : String(e));

/** Registers a module's finish hook, read from inside its own provider, with the wizard. */
function FinishRegistrar({ moduleId, useFinish, register }: { moduleId: string; useFinish: () => () => Promise<void>; register: (id: string, run: () => Promise<void>) => void }) {
  const run = useFinish();
  useEffect(() => {
    register(moduleId, run);
  });
  return null;
}

/** Calls the active step's useCanProceed inside its module's provider and hands the answer to `children`. */
function ProceedGate({ useCanProceed, children }: { useCanProceed: (() => boolean) | undefined; children: (open: boolean) => ReactNode }) {
  return useCanProceed ? <GateWithHook useCanProceed={useCanProceed}>{children}</GateWithHook> : <>{children(true)}</>;
}
// The prop is named use* so the React Compiler and rules-of-hooks treat the call as a hook
// (a bare `use` would be memoized as a plain call and never see the context change).
function GateWithHook({ useCanProceed, children }: { useCanProceed: () => boolean; children: (open: boolean) => ReactNode }) {
  return <>{children(useCanProceed())}</>;
}

/**
 * The first-run wizard host (F9-03 prep): the dialog, stepper and Back, Next and
 * Complete over every mounted module's setup steps, in module ID order. Each module's
 * provider wraps the whole dialog so its steps share one draft; a step gets onNext;
 * Next is held by the active step's useCanProceed; Complete runs each module's
 * useFinish apply in order and stops at the first that throws.
 *
 * Who asks whether setup is needed (each module's isRequired, after login) is the
 * app's gate, not this host.
 */
export function SetupWizard({ onComplete }: { onComplete: () => void }) {
  const { displayName } = useShellContext();
  const { modules } = useMountedModules();
  const mods = useMemo(
    () => [...modules].filter((m) => m.setup !== undefined).sort((a, b) => a.id.localeCompare(b.id)),
    [modules],
  );
  const steps = useMemo<StepEntry[]>(
    () => mods.flatMap((m) => (m.setup?.steps ?? []).map((s) => ({ moduleId: m.id, id: s.id, title: s.title, step: s as Setup['steps'][number] }))),
    [mods],
  );
  const finishers = useRef(new Map<string, () => Promise<void>>());
  const register = useCallback((id: string, run: () => Promise<void>) => {
    finishers.current.set(id, run);
  }, []);
  const [active, setActive] = useState(0);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const last = steps.length - 1;
  const onNext = useCallback(() => setActive((i) => Math.min(i + 1, Math.max(last, 0))), [last]);
  const current = steps[Math.min(active, Math.max(last, 0))] as StepEntry | undefined;

  const finish = async () => {
    setSaving(true);
    setError(null);
    try {
      for (const m of mods) {
        if (!m.setup?.useFinish) continue;
        try {
          await finishers.current.get(m.id)?.();
        } catch (e) {
          throw new Error(`${m.manifest.displayName}: ${message(e)}`, { cause: e });
        }
      }
      onComplete();
    } catch (e) {
      setError(message(e));
    } finally {
      setSaving(false);
    }
  };

  if (steps.length === 0) {
    return (
      <Dialog open>
        <DialogTitle>Welcome to {displayName}</DialogTitle>
        <DialogContent>
          <Typography>There is nothing to set up.</Typography>
        </DialogContent>
        <DialogActions>
          <Button variant="contained" onClick={onComplete}>
            Continue
          </Button>
        </DialogActions>
      </Dialog>
    );
  }

  const body = (
    <Dialog open maxWidth="md" fullWidth>
      <DialogTitle>Welcome to {displayName}</DialogTitle>
      <DialogContent>
        <Stepper activeStep={Math.min(active, last)} sx={{ mb: 4 }}>
          {steps.map((s) => (
            <Step key={`${s.moduleId}.${s.id}`}>
              <StepLabel>{s.title}</StepLabel>
            </Step>
          ))}
        </Stepper>
        {current && (
          <LazyWithProps<SetupStepProps> key={`${current.moduleId}.${current.id}`} load={current.step.lazy} label={`${current.moduleId} setup ${current.id}`} props={{ onNext }} />
        )}
        {error && (
          <Alert severity="error" sx={{ mt: 2 }}>
            {error}
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={() => setActive((i) => Math.max(i - 1, 0))} disabled={active === 0 || saving}>
          Back
        </Button>
        <Box sx={{ flex: 1 }} />
        {active >= last ? (
          <Button variant="contained" onClick={() => void finish()} disabled={saving} startIcon={saving ? <CircularProgress size={16} /> : undefined}>
            {saving ? 'Saving...' : 'Complete Setup'}
          </Button>
        ) : (
          <ProceedGate key={`${current?.moduleId}.${current?.id}`} useCanProceed={current?.step.useCanProceed}>
            {(open) => (
              <Button variant="contained" onClick={onNext} disabled={!open}>
                Next
              </Button>
            )}
          </ProceedGate>
        )}
      </DialogActions>
    </Dialog>
  );

  // Each module's provider wraps the whole dialog (a portal keeps context), outermost first in ID order.
  return mods.reduceRight<ReactNode>((inner, m) => {
    const P = m.setup?.provider;
    const useFinish = m.setup?.useFinish;
    const reg = useFinish ? <FinishRegistrar moduleId={m.id} useFinish={useFinish} register={register} /> : null;
    return P ? (
      <P>
        {reg}
        {inner}
      </P>
    ) : (
      <>
        {reg}
        {inner}
      </>
    );
  }, body);
}
