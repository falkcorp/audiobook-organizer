// file: core/web/packages/shell/test/pages/settings.test.tsx
// version: 0.1.0
// guid: e2b95c61-0d38-4f74-b6a1-8c3f1d7a5e20
// last-edited: 2026-11-22

// F9-03 prep: a module's settingsSession gives its panels one shared form
// (dirty state, one save, discard), plus banners, a footer and a dialog host
// that outlive a tab, and the shell page guards navigation and unload while it
// is dirty. (Gaps 1 to 4 of seams.d/F9-04-s6-settings.md.)

import { createContext, useContext, useState, type ReactNode } from 'react';
import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { Route, Routes } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import { defineModule } from '../../src/defineModule';
import { SettingsPage } from '../../src/app/shellPages';
import { renderInShell } from '../../src/testing';
import type { SettingsSession } from '../../src/types';

const manifest = {
  id: 'gotham',
  version: '0.1.0',
  displayName: 'Gotham',
  pages: [],
  nav: [],
  settingsPanels: ['library', 'paths'],
  widgets: [],
  banners: [],
  topBarItems: [],
  userPage: [],
  activityPanes: [],
  setupSteps: [],
  permissions: [],
} as const;

interface Form {
  name: string;
  setName: (n: string) => void;
  saved: string;
  dialog: boolean;
  setDialog: (o: boolean) => void;
  save: () => Promise<void>;
  discard: () => void;
  saving: boolean;
}
const FormContext = createContext<Form | null>(null);
const useForm = () => {
  const f = useContext(FormContext);
  if (!f) throw new Error('outside provider');
  return f;
};

function makeModule(opts: { footer?: boolean; onSave?: (name: string) => Promise<void> } = {}) {
  function Provider({ children }: { children: ReactNode }) {
    const [saved, setSaved] = useState('Batman');
    const [name, setName] = useState('Batman');
    const [dialog, setDialog] = useState(false);
    const [saving, setSaving] = useState(false);
    const save = async () => {
      setSaving(true);
      try {
        await opts.onSave?.(name);
        setSaved(name);
      } finally {
        setSaving(false);
      }
    };
    const discard = () => setName(saved);
    return <FormContext.Provider value={{ name, setName, saved, dialog, setDialog, save, discard, saving }}>{children}</FormContext.Provider>;
  }
  const LibraryPanel = () => {
    const f = useForm();
    return (
      <div>
        <input aria-label="name" value={f.name} onChange={(e) => f.setName(e.target.value)} />
        <button type="button" onClick={() => f.setDialog(true)}>
          open browser
        </button>
      </div>
    );
  };
  const PathsPanel = () => <p data-testid="paths">paths sees {useForm().name}</p>;
  return defineModule(manifest, {
    routes: [],
    nav: [],
    permissionsHints: { view: [] },
    settingsPanels: [
      { id: 'library', label: 'Library', lazy: async () => ({ default: LibraryPanel }) },
      { id: 'paths', label: 'Paths', lazy: async () => ({ default: PathsPanel }) },
    ],
    settingsSession: {
      Provider,
      useSession: (): SettingsSession => {
        const f = useForm();
        return { dirty: f.name !== f.saved, saving: f.saving, save: f.save, discard: f.discard };
      },
      Banners: () => <p>Settings saved banner slot</p>,
      Dialogs: () => {
        const f = useForm();
        return f.dialog ? <div role="dialog">Folder browser</div> : null;
      },
      ...(opts.footer ? { Footer: () => <div data-testid="books-footer">books footer</div> } : {}),
    },
  });
}

function renderSettings(mod: ReturnType<typeof makeModule>, route = '/settings/gotham.library') {
  return renderInShell(
    <Routes>
      <Route path="settings/*" element={<SettingsPage />} />
      <Route path="*" element={<p data-testid="elsewhere">elsewhere</p>} />
    </Routes>,
    { modules: [mod], permissions: [], route },
  );
}

describe('SettingsPage session', () => {
  it('shares one form across panels, so an edit in one is seen in another after a tab switch', async () => {
    const r = renderSettings(makeModule());
    fireEvent.change(await screen.findByLabelText('name'), { target: { value: 'Robin' } });
    await act(async () => {
      await r.router.navigate('/settings/gotham.paths');
    });
    expect((await screen.findByTestId('paths')).textContent).toBe('paths sees Robin');
  });

  it('renders the banners and the dialog host above every tab, and a dialog survives a tab switch', async () => {
    const r = renderSettings(makeModule());
    expect(await screen.findByText('Settings saved banner slot')).toBeInTheDocument();
    fireEvent.click(await screen.findByRole('button', { name: 'open browser' }));
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    await act(async () => {
      await r.router.navigate('/settings/gotham.paths');
    });
    await screen.findByTestId('paths');
    expect(screen.getByRole('dialog')).toHaveTextContent('Folder browser');
    expect(screen.getByText('Settings saved banner slot')).toBeInTheDocument();
  });

  it("shows the module's own footer when it has one, and no default save bar", async () => {
    renderSettings(makeModule({ footer: true }));
    fireEvent.change(await screen.findByLabelText('name'), { target: { value: 'Robin' } });
    expect(screen.getByTestId('books-footer')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save settings' })).not.toBeInTheDocument();
  });

  it('shows a default Save / Discard bar only while dirty, and one Save runs the session save', async () => {
    const onSave = vi.fn(async () => {});
    renderSettings(makeModule({ onSave }));
    const input = await screen.findByLabelText('name');
    expect(screen.queryByRole('button', { name: 'Save settings' })).not.toBeInTheDocument();
    fireEvent.change(input, { target: { value: 'Robin' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save settings' }));
    await waitFor(() => expect(onSave).toHaveBeenCalledWith('Robin'));
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Save settings' })).not.toBeInTheDocument());
  });

  it('Discard resets the form', async () => {
    renderSettings(makeModule());
    const input = await screen.findByLabelText('name');
    fireEvent.change(input, { target: { value: 'Robin' } });
    fireEvent.click(screen.getByRole('button', { name: 'Discard' }));
    await waitFor(() => expect((screen.getByLabelText('name') as HTMLInputElement).value).toBe('Batman'));
  });

  it('warns on unload only while dirty', async () => {
    renderSettings(makeModule());
    const input = await screen.findByLabelText('name');
    const clean = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(clean);
    expect(clean.defaultPrevented).toBe(false);
    fireEvent.change(input, { target: { value: 'Robin' } });
    const dirty = new Event('beforeunload', { cancelable: true });
    window.dispatchEvent(dirty);
    expect(dirty.defaultPrevented).toBe(true);
  });

  it('blocks leaving the Settings page while dirty; Stay keeps the page, Leave goes', async () => {
    const r = renderSettings(makeModule());
    fireEvent.change(await screen.findByLabelText('name'), { target: { value: 'Robin' } });
    await act(async () => {
      void r.router.navigate('/elsewhere');
    });
    expect(await screen.findByText('Unsaved changes')).toBeInTheDocument();
    expect(r.router.state.location.pathname).toBe('/settings/gotham.library');
    fireEvent.click(screen.getByRole('button', { name: 'Stay' }));
    await waitFor(() => expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument());
    expect(r.router.state.location.pathname).toBe('/settings/gotham.library');
    await act(async () => {
      void r.router.navigate('/elsewhere');
    });
    fireEvent.click(await screen.findByRole('button', { name: 'Leave without saving' }));
    await waitFor(() => expect(r.router.state.location.pathname).toBe('/elsewhere'));
  });

  it('Save and leave saves first, then goes', async () => {
    const onSave = vi.fn(async () => {});
    const r = renderSettings(makeModule({ onSave }));
    fireEvent.change(await screen.findByLabelText('name'), { target: { value: 'Robin' } });
    await act(async () => {
      void r.router.navigate('/elsewhere');
    });
    fireEvent.click(await screen.findByRole('button', { name: 'Save and leave' }));
    await waitFor(() => expect(r.router.state.location.pathname).toBe('/elsewhere'));
    expect(onSave).toHaveBeenCalledWith('Robin');
  });

  it('a clean form never blocks, and a tab switch inside Settings is never blocked', async () => {
    const r = renderSettings(makeModule());
    fireEvent.change(await screen.findByLabelText('name'), { target: { value: 'Robin' } });
    await act(async () => {
      await r.router.navigate('/settings/gotham.paths');
    });
    expect(r.router.state.location.pathname).toBe('/settings/gotham.paths');
    expect(screen.queryByText('Unsaved changes')).not.toBeInTheDocument();
  });

  it('a failed save keeps the user on the page and shows the error', async () => {
    const onSave = vi.fn(async () => {
      throw new Error('config write refused');
    });
    const r = renderSettings(makeModule({ onSave }));
    fireEvent.change(await screen.findByLabelText('name'), { target: { value: 'Robin' } });
    await act(async () => {
      void r.router.navigate('/elsewhere');
    });
    fireEvent.click(await screen.findByRole('button', { name: 'Save and leave' }));
    // Shown in the prompt and in the page's save bar.
    expect((await screen.findAllByText(/config write refused/)).length).toBeGreaterThan(0);
    expect(r.router.state.location.pathname).toBe('/settings/gotham.library');
  });
});
