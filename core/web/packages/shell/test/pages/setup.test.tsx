// file: core/web/packages/shell/test/pages/setup.test.tsx
// version: 0.1.0
// guid: a47d3e19-6c20-4b85-9e1f-2d8b0c5a7f36
// last-edited: 2026-11-22

// F9-03 prep: SetupContribution steps get onNext, a per-step useCanProceed,
// a module-wide provider, and a module finish hook that Complete runs in
// module ID order. (Gaps 1 to 3 of seams.d/F9-04-s5-setup.md.)

import { createContext, useContext, useState, type ReactNode } from 'react';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { defineModule } from '../../src/defineModule';
import { SetupWizard } from '../../src/app/shellPages';
import { renderInShell } from '../../src/testing';

const base = {
  version: '0.1.0',
  pages: [],
  nav: [],
  settingsPanels: [],
  widgets: [],
  banners: [],
  topBarItems: [],
  userPage: [],
  activityPanes: [],
  permissions: [],
} as const;

interface Draft {
  path: string;
  setPath: (p: string) => void;
}
const DraftContext = createContext<Draft | null>(null);
const useDraft = () => {
  const d = useContext(DraftContext);
  if (!d) throw new Error('step outside the module provider');
  return d;
};
function DraftProvider({ children }: { children: ReactNode }) {
  const [path, setPath] = useState('');
  return <DraftContext.Provider value={{ path, setPath }}>{children}</DraftContext.Provider>;
}

function PathStep() {
  const { path, setPath } = useDraft();
  return <input aria-label="library path" value={path} onChange={(e) => setPath(e.target.value)} />;
}
function ToolsStep({ onNext }: { onNext: () => void }) {
  return (
    <button type="button" onClick={onNext}>
      Skip tools
    </button>
  );
}

function gotham(order: string[], opts: { fail?: boolean } = {}) {
  return defineModule(
    { ...base, id: 'gotham', displayName: 'Gotham', setupSteps: ['path', 'tools', 'ai'] } as const,
    {
      routes: [],
      nav: [],
      settingsPanels: [],
      permissionsHints: { view: [] },
      setup: {
        isRequired: async () => true,
        provider: DraftProvider,
        steps: [
          { id: 'path', title: 'Library path', lazy: async () => ({ default: PathStep }), useCanProceed: () => useDraft().path.trim() !== '' },
          { id: 'tools', title: 'Tools', lazy: async () => ({ default: ToolsStep }) },
          { id: 'ai', title: 'AI', lazy: async () => ({ default: () => <p>AI step</p> }) },
        ],
        useFinish: () => {
          const { path } = useDraft();
          return async () => {
            order.push(`gotham:${path}`);
            if (opts.fail) throw new Error('disk is full');
          };
        },
      },
    },
  );
}

function arkham(order: string[]) {
  return defineModule(
    { ...base, id: 'arkham', displayName: 'Arkham', setupSteps: ['wards'] } as const,
    {
      routes: [],
      nav: [],
      settingsPanels: [],
      permissionsHints: { view: [] },
      setup: {
        isRequired: async () => true,
        steps: [{ id: 'wards', title: 'Wards', lazy: async () => ({ default: () => <p>Wards step</p> }) }],
        useFinish: () => async () => {
          order.push('arkham');
        },
      },
    },
  );
}

function twoGated() {
  return defineModule({ ...base, id: 'gotham', displayName: 'Gotham', setupSteps: ['a', 'b', 'c'] } as const, {
    routes: [],
    nav: [],
    settingsPanels: [],
    permissionsHints: { view: [] },
    setup: {
      isRequired: async () => true,
      provider: DraftProvider,
      steps: [
        // Different hook bodies on adjacent steps: one gate instance reused across them would call a different number of hooks.
        {
          id: 'a',
          title: 'A',
          lazy: async () => ({ default: PathStep }),
          useCanProceed: () => {
            const { path } = useDraft();
            const [floor] = useState(0);
            const [ceiling] = useState(100);
            return path.trim().length > floor && path.length < ceiling;
          },
        },
        {
          id: 'b',
          title: 'B',
          lazy: async () => ({ default: () => <p>B body</p> }),
          useCanProceed: () => {
            const [min] = useState(3);
            return useDraft().path.length > min;
          },
        },
        { id: 'c', title: 'C', lazy: async () => ({ default: () => <p>C body</p> }) },
      ],
    },
  });
}

describe('SetupWizard slots', () => {
  it('moves between adjacent steps that have different useCanProceed hooks', async () => {
    renderInShell(<SetupWizard onComplete={vi.fn()} />, { modules: [twoGated()], permissions: [] });
    fireEvent.change(await screen.findByLabelText('library path'), { target: { value: '/vault' } });
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    expect(await screen.findByText('B body')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Next' })).toBeEnabled();
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    expect(await screen.findByText('C body')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Complete Setup' })).toBeInTheDocument();
  });

  it('blocks Next while the active step says it cannot proceed, and opens it when the shared state changes', async () => {
    renderInShell(<SetupWizard onComplete={vi.fn()} />, { modules: [gotham([])], permissions: [] });
    const next = await screen.findByRole('button', { name: 'Next' });
    expect(next).toBeDisabled();
    fireEvent.change(await screen.findByLabelText('library path'), { target: { value: '/vault' } });
    expect(screen.getByRole('button', { name: 'Next' })).toBeEnabled();
  });

  it('hands a step onNext, so a step can offer Skip', async () => {
    renderInShell(<SetupWizard onComplete={vi.fn()} />, { modules: [gotham([])], permissions: [] });
    fireEvent.change(await screen.findByLabelText('library path'), { target: { value: '/vault' } });
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Skip tools' }));
    expect(await screen.findByText('AI step')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Complete Setup' })).toBeInTheDocument();
  });

  it('runs every module finish hook in module ID order, from inside its own provider, then onComplete', async () => {
    const order: string[] = [];
    const onComplete = vi.fn();
    renderInShell(<SetupWizard onComplete={onComplete} />, { modules: [gotham(order), arkham(order)], permissions: [] });
    // Steps run in module ID order: arkham's wards, then gotham's path, tools, ai.
    expect(await screen.findByText('Wards step')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    fireEvent.change(await screen.findByLabelText('library path'), { target: { value: '/vault' } });
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Skip tools' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Complete Setup' }));
    await waitFor(() => expect(onComplete).toHaveBeenCalledTimes(1));
    expect(order).toEqual(['arkham', 'gotham:/vault']);
  });

  it('a failing finish shows its message, runs no later module, and does not complete', async () => {
    const order: string[] = [];
    const onComplete = vi.fn();
    // arkham sorts first and succeeds; gotham fails; a third module after it must not run.
    const zeta = defineModule({ ...base, id: 'zeta', displayName: 'Zeta', setupSteps: ['z'] } as const, {
      routes: [],
      nav: [],
      settingsPanels: [],
      permissionsHints: { view: [] },
      setup: { isRequired: async () => true, steps: [{ id: 'z', title: 'Z', lazy: async () => ({ default: () => null }) }], useFinish: () => async () => void order.push('zeta') },
    });
    renderInShell(<SetupWizard onComplete={onComplete} />, { modules: [gotham(order, { fail: true }), arkham(order), zeta], permissions: [] });
    await screen.findByText('Wards step');
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    fireEvent.change(await screen.findByLabelText('library path'), { target: { value: '/vault' } });
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Skip tools' }));
    await screen.findByText('AI step');
    fireEvent.click(screen.getByRole('button', { name: 'Next' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Complete Setup' }));
    expect(await screen.findByText(/disk is full/)).toBeInTheDocument();
    expect(order).toEqual(['arkham', 'gotham:/vault']);
    expect(onComplete).not.toHaveBeenCalled();
    // The button is usable again for a retry.
    expect(screen.getByRole('button', { name: 'Complete Setup' })).toBeEnabled();
  });

  it('titles the wizard with the app name', async () => {
    renderInShell(<SetupWizard onComplete={vi.fn()} />, { modules: [gotham([])], permissions: [] });
    expect(await screen.findByText('Welcome to test')).toBeInTheDocument();
  });

  it('says so when no module has a setup step', async () => {
    renderInShell(<SetupWizard onComplete={vi.fn()} />, { modules: [], permissions: [] });
    expect(await screen.findByText(/nothing to set up/i)).toBeInTheDocument();
  });
});
