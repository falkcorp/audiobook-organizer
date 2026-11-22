// file: web/src/hooks/__tests__/signInRequiredActions.test.tsx
// version: 0.1.0
// guid: 52bdfd67-0ffa-4288-9105-30f5f5d4bcc4
// last-edited: 2026-11-22

// SEC5-8 on the non-Users actions: temp-login, tool install, update apply, plugin settings,
// import-path add (file browser, settings handlers), backup restore (settings handlers) and the
// setup wizard's tool install. Under auth-off each shows "Turn on sign-in to do this" with a link
// and is disabled up front; a refusal that arrives anyway maps to the same message; with auth on
// nothing changes.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, renderHook, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { ApiError } from '../../services/apiError';
import { SIGN_IN_HELP_URL } from '../../utils/signInRequired';
import { coreJson, coreProblem, stubCoreFetch } from '../../test/coreFetch';

const authState = vi.hoisted(() => ({
  value: { initialized: true, authEnabled: true } as { initialized: boolean; authEnabled: boolean | null } | null,
}));
vi.mock('../../contexts/AuthContext', () => ({
  useOptionalAuth: () => authState.value,
}));
vi.mock('../../components/common/ServerFileBrowser', () => ({
  ServerFileBrowser: ({ onSelect }: { onSelect: (p: string, d: boolean) => void }) => (
    <button onClick={() => onSelect('/mnt/batcave/audio', true)}>pick folder</button>
  ),
}));
const toolApi = vi.hoisted(() => ({
  getTools: vi.fn(),
  installTool: vi.fn(),
  applyUpdate: vi.fn(),
  checkForUpdate: vi.fn(),
  addImportPath: vi.fn(),
  restoreBackup: vi.fn(),
}));
vi.mock('../../services/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../services/api')>()),
  ...toolApi,
}));

import { useSignInRequiredForAction } from '../useSignInRequiredForAction';
import { TempLoginTab } from '../../components/settings/TempLoginTab';
import { ToolsPanel } from '../../components/tools/ToolsPanel';
import { AutoUpdateSection } from '../../components/settings/AutoUpdateSection';
import PluginsTab from '../../components/settings/PluginsTab';
import { FileBrowser } from '../../pages/FileBrowser';
import { installRecommendedTools } from '../../modules/books/pages/installRecommendedTools';
import { useImportFolderHandlers } from '../useImportFolderHandlers';
import { useBackupHandlers } from '../useBackupHandlers';

vi.mock('../../components/toast/ToastProvider', () => ({
  useToast: () => ({ toast: vi.fn() }),
}));

const refusal = (code: string) => new ApiError('raw 403 text', 403, { code });
const NOTICE = /Turn on sign-in to do this/;
const authOff = () => {
  authState.value = { initialized: true, authEnabled: false };
};
const authOn = () => {
  authState.value = { initialized: true, authEnabled: true };
};

beforeEach(() => {
  vi.clearAllMocks();
  authOn();
});
afterEach(() => vi.unstubAllGlobals());

describe('useSignInRequiredForAction', () => {
  it('blocks under auth-off and offers the notice with the link', () => {
    authOff();
    const { result } = renderHook(() => useSignInRequiredForAction());
    expect(result.current.blocked).toBe(true);
    render(<>{result.current.notice}</>);
    expect(screen.getByRole('link')).toHaveAttribute('href', SIGN_IN_HELP_URL);
  });

  it('does not block with auth on, unknown, or no provider', () => {
    for (const v of [{ initialized: true, authEnabled: true }, { initialized: true, authEnabled: null }, null]) {
      authState.value = v;
      const { result } = renderHook(() => useSignInRequiredForAction());
      expect(result.current.blocked).toBe(false);
      expect(result.current.notice).toBeNull();
    }
  });

  it('does not block before the auth status has loaded', () => {
    authState.value = { initialized: false, authEnabled: false };
    const { result } = renderHook(() => useSignInRequiredForAction());
    expect(result.current.blocked).toBe(false);
  });

  it('refuse() takes the books FORBIDDEN 403 under auth-off, and ignores it with auth on', () => {
    authOff();
    const off = renderHook(() => useSignInRequiredForAction());
    expect(off.result.current.refuse(refusal('FORBIDDEN'))).toBe(true);
    authOn();
    const on = renderHook(() => useSignInRequiredForAction());
    expect(on.result.current.refuse(refusal('FORBIDDEN'))).toBe(false);
    expect(on.result.current.blocked).toBe(false);
  });

  it('refuse() takes interactive_required whatever the auth setting, and then blocks', () => {
    authState.value = { initialized: true, authEnabled: null };
    const { result } = renderHook(() => useSignInRequiredForAction());
    let taken = false;
    act(() => {
      taken = result.current.refuse(refusal('interactive_required'));
    });
    expect(taken).toBe(true);
    expect(result.current.blocked).toBe(true);
  });
});

describe('temp-login mint (POST /auth/temp-tokens, now core temp-login)', () => {
  const serve = (mint: Response) =>
    stubCoreFetch({
      'GET /users': () =>
        coreJson({ items: [{ id: 'u1', username: 'bruce.wayne', roles: [], status: 'active' }], total: 1 }),
      'POST /users/u1/temp-login': mint,
    });

  it('under auth-off shows the message and link and disables Generate', async () => {
    authOff();
    const { calls } = serve(coreJson({ link: '/login/temp#token=x' }));
    render(<TempLoginTab />);
    expect(await screen.findByText(NOTICE)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /turn on sign-in/i })).toHaveAttribute('href', SIGN_IN_HELP_URL);
    expect(screen.getByRole('button', { name: 'Generate Login URL' })).toBeDisabled();
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0);
  });

  it('maps a 403 interactive_required to the message instead of the raw text', async () => {
    const user = userEvent.setup();
    serve(coreProblem(403, 'interactive_required', 'raw interactive refusal'));
    render(<TempLoginTab />);
    const btn = await screen.findByRole('button', { name: 'Generate Login URL' });
    await waitFor(() => expect(btn).toBeEnabled());
    await user.click(btn);
    expect(await screen.findByText(NOTICE)).toBeInTheDocument();
    expect(screen.queryByText(/raw interactive refusal/)).toBeNull();
  });

  it('with auth on and an ordinary failure, shows the error as before', async () => {
    const user = userEvent.setup();
    serve(coreProblem(500, 'internal', 'the batmobile is down'));
    render(<TempLoginTab />);
    const btn = await screen.findByRole('button', { name: 'Generate Login URL' });
    await waitFor(() => expect(btn).toBeEnabled());
    await user.click(btn);
    expect(await screen.findByText('the batmobile is down')).toBeInTheDocument();
    expect(screen.queryByText(NOTICE)).toBeNull();
  });
});

describe('tool install (POST /tools/:name/install)', () => {
  beforeEach(() => {
    toolApi.getTools.mockResolvedValue([{ name: 'fpcalc', mode: 'system', available: false }]);
  });

  it('under auth-off disables Install and shows the message', async () => {
    authOff();
    render(<ToolsPanel mode="settings" />);
    expect(await screen.findByText(NOTICE)).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Install' })).toBeDisabled();
  });

  it('maps a 403 interactive_required to the message', async () => {
    const user = userEvent.setup();
    toolApi.installTool.mockRejectedValue(refusal('interactive_required'));
    render(<ToolsPanel mode="settings" />);
    await user.click(await screen.findByRole('button', { name: 'Install' }));
    expect(await screen.findByText(NOTICE)).toBeInTheDocument();
    expect(screen.queryByText('raw 403 text')).toBeNull();
  });

  it('with auth on keeps the button live and shows an ordinary failure raw', async () => {
    const user = userEvent.setup();
    toolApi.installTool.mockRejectedValue(new Error('download failed'));
    render(<ToolsPanel mode="settings" />);
    await user.click(await screen.findByRole('button', { name: 'Install' }));
    expect(await screen.findByText('download failed')).toBeInTheDocument();
  });
});

describe('update apply (POST /update/apply)', () => {
  const settings = { autoUpdateEnabled: false, autoUpdateChannel: 'stable', autoUpdateCheckMinutes: 60 };
  const ready = () =>
    toolApi.checkForUpdate.mockResolvedValue({
      current_version: 'v1.0.0',
      latest_version: 'v1.1.0',
      update_available: true,
    });
  const openApply = async (user: ReturnType<typeof userEvent.setup>) => {
    await user.click(screen.getByRole('button', { name: /check now/i }));
    await user.click(await screen.findByRole('button', { name: /update now/i }));
  };

  it('under auth-off disables Update Now and shows the message', async () => {
    authOff();
    ready();
    const user = userEvent.setup();
    render(<AutoUpdateSection settings={settings} setSettings={vi.fn()} />);
    await user.click(screen.getByRole('button', { name: /check now/i }));
    expect(await screen.findByRole('button', { name: /update now/i })).toBeDisabled();
    expect(screen.getByText(NOTICE)).toBeInTheDocument();
  });

  it('maps a 403 interactive_required refusal to the message', async () => {
    ready();
    toolApi.applyUpdate.mockRejectedValue(refusal('interactive_required'));
    const user = userEvent.setup();
    render(<AutoUpdateSection settings={settings} setSettings={vi.fn()} />);
    await openApply(user);
    const dialog = await screen.findByRole('dialog');
    await user.click(
      Array.from(dialog.querySelectorAll('button')).find((b) => /apply|update/i.test(b.textContent ?? ''))!,
    );
    expect(await screen.findByText(NOTICE)).toBeInTheDocument();
    expect(screen.queryByText('raw 403 text')).toBeNull();
  });
});

describe('plugin settings (PUT /plugins/:id/settings)', () => {
  const plugin = { id: 'batsignal', name: 'Bat Signal', version: '1.0.0', capabilities: [], enabled: true, initialized: true, health: 'ok' };
  const stubPlugins = (put: Response) =>
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith('/settings') && init?.method === 'PUT') return put.clone();
        return new Response(JSON.stringify({ data: { plugins: [plugin] } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }),
    );
  const openRow = async (user: ReturnType<typeof userEvent.setup>) => {
    render(
      <MemoryRouter>
        <PluginsTab />
      </MemoryRouter>,
    );
    await screen.findByText('Bat Signal');
    const buttons = screen.getAllByRole('button');
    await user.click(buttons[buttons.length - 1]);
  };

  it('under auth-off shows the message and disables Save', async () => {
    authOff();
    stubPlugins(new Response('{}', { status: 200 }));
    await openRow(userEvent.setup());
    expect(await screen.findByText(NOTICE)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  });

  it('maps a 403 interactive_required to the message when the auth setting is unknown', async () => {
    authState.value = { initialized: true, authEnabled: null };
    stubPlugins(new Response(JSON.stringify({ code: 'interactive_required', error: 'raw plugin refusal' }), { status: 403 }));
    const user = userEvent.setup();
    await openRow(user);
    await user.click(await screen.findByRole('button', { name: 'Save' }));
    expect(await screen.findByText(NOTICE)).toBeInTheDocument();
    expect(screen.queryByText(/raw plugin refusal/)).toBeNull();
    expect(screen.queryByText(/HTTP 403/)).toBeNull();
  });

  it('with auth on keeps an ordinary 403 as the error text', async () => {
    stubPlugins(new Response(JSON.stringify({ code: 'FORBIDDEN', error: 'nope' }), { status: 403 }));
    const user = userEvent.setup();
    await openRow(user);
    await user.click(await screen.findByRole('button', { name: 'Save' }));
    expect(await screen.findByText(/Failed to save settings/)).toBeInTheDocument();
    expect(screen.queryByText(NOTICE)).toBeNull();
  });
});

describe('import-path add (POST /import-paths)', () => {
  const renderBrowser = () =>
    render(
      <MemoryRouter>
        <FileBrowser />
      </MemoryRouter>,
    );

  it('file browser under auth-off shows the message and disables Add as Import Path', async () => {
    authOff();
    const user = userEvent.setup();
    renderBrowser();
    await user.click(await screen.findByRole('button', { name: 'pick folder' }));
    expect(screen.getByText(NOTICE)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /add as import path/i })).toBeDisabled();
    expect(toolApi.addImportPath).not.toHaveBeenCalled();
  });

  it('file browser maps a 403 interactive_required refusal to the message', async () => {
    toolApi.addImportPath.mockRejectedValue(refusal('interactive_required'));
    const user = userEvent.setup();
    renderBrowser();
    await user.click(await screen.findByRole('button', { name: 'pick folder' }));
    await user.click(screen.getByRole('button', { name: /add as import path/i }));
    expect(await screen.findByText(NOTICE)).toBeInTheDocument();
    expect(screen.queryByText('raw 403 text')).toBeNull();
  });

  it('file browser with auth on keeps an ordinary failure as the notice text', async () => {
    toolApi.addImportPath.mockRejectedValue(new Error('path is not readable'));
    const user = userEvent.setup();
    renderBrowser();
    await user.click(await screen.findByRole('button', { name: 'pick folder' }));
    await user.click(screen.getByRole('button', { name: /add as import path/i }));
    expect(await screen.findByText('path is not readable')).toBeInTheDocument();
  });

  it('settings handler hands the refusal to the gate and does not log it as a failure', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {});
    toolApi.addImportPath.mockRejectedValue(refusal('FORBIDDEN'));
    const refuse = vi.fn(() => true);
    const { result } = renderHook(() =>
      useImportFolderHandlers({
        setImportFolders: vi.fn(),
        setScanStatuses: vi.fn(),
        setCancelScanTarget: vi.fn(),
        setScanErrorTarget: vi.fn(),
        setNewFolderPath: vi.fn(),
        setShowFolderBrowser: vi.fn(),
        setAddFolderDialogOpen: vi.fn(),
        scanIntervalsRef: { current: {} },
        cancelScanTarget: null,
        scanStatuses: {},
        newFolderPath: '/mnt/batcave/audio',
        refuse,
      }),
    );
    await act(() => result.current.handleAddImportFolder());
    expect(refuse).toHaveBeenCalledTimes(1);
    expect(spy).not.toHaveBeenCalled();
    spy.mockRestore();
  });
});

describe('backup restore (POST /backup/restore)', () => {
  it('settings handler hands the refusal to the gate and shows no failure notice', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {});
    toolApi.restoreBackup.mockRejectedValue(refusal('FORBIDDEN'));
    const refuse = vi.fn(() => true);
    const setBackupNotice = vi.fn();
    const setRestoreDialogOpen = vi.fn();
    const { result } = renderHook(() =>
      useBackupHandlers({
        setBackups: vi.fn(),
        setBackupNotice,
        setBackupsLoading: vi.fn(),
        setRestoreTarget: vi.fn(),
        setRestoreDialogOpen,
        setRestoreInProgress: vi.fn(),
        setDeleteBackupTarget: vi.fn(),
        setDeleteBackupInProgress: vi.fn(),
        setCreateBackupInProgress: vi.fn(),
        setNewFolderPath: vi.fn(),
        restoreTarget: { filename: 'backup-1.tar.gz' } as never,
        restoreVerify: true,
        deleteBackupTarget: null,
        refuse,
      }),
    );
    await act(() => result.current.handleConfirmRestore());
    expect(refuse).toHaveBeenCalledTimes(1);
    expect(setBackupNotice).toHaveBeenCalledTimes(1);
    expect(setBackupNotice).toHaveBeenCalledWith(null);
    expect(setRestoreDialogOpen).not.toHaveBeenCalledWith(false);
    spy.mockRestore();
  });

  it('with no gate, a failed restore still reports "Restore refused"', async () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {});
    toolApi.restoreBackup.mockRejectedValue(new ApiError('checksum mismatch', 409));
    const setBackupNotice = vi.fn();
    const { result } = renderHook(() =>
      useBackupHandlers({
        setBackups: vi.fn(),
        setBackupNotice,
        setBackupsLoading: vi.fn(),
        setRestoreTarget: vi.fn(),
        setRestoreDialogOpen: vi.fn(),
        setRestoreInProgress: vi.fn(),
        setDeleteBackupTarget: vi.fn(),
        setDeleteBackupInProgress: vi.fn(),
        setCreateBackupInProgress: vi.fn(),
        setNewFolderPath: vi.fn(),
        restoreTarget: { filename: 'backup-1.tar.gz' } as never,
        restoreVerify: true,
        deleteBackupTarget: null,
      }),
    );
    await act(() => result.current.handleConfirmRestore());
    expect(setBackupNotice).toHaveBeenLastCalledWith({
      severity: 'error',
      message: 'Restore refused: checksum mismatch',
    });
    spy.mockRestore();
  });
});

describe('setup wizard tool install (installRecommendedTools)', () => {
  it('under auth-off attempts nothing and names the sign-in requirement', async () => {
    const notify = vi.fn();
    await installRecommendedTools(notify, true);
    expect(toolApi.installTool).not.toHaveBeenCalled();
    expect(notify).toHaveBeenCalledTimes(1);
    expect(notify.mock.calls[0][0]).toMatch(NOTICE);
  });

  it('maps a refusal that arrives anyway and stops after the first tool', async () => {
    toolApi.installTool.mockRejectedValue(refusal('interactive_required'));
    const notify = vi.fn();
    await installRecommendedTools(notify);
    expect(toolApi.installTool).toHaveBeenCalledTimes(1);
    expect(notify.mock.calls.at(-1)![0]).toMatch(NOTICE);
    expect(notify.mock.calls.some((c) => /raw 403/.test(c[0]))).toBe(false);
  });

  it('with auth on installs both tools as before', async () => {
    toolApi.installTool.mockResolvedValue({ path: '/x', version: '1' });
    const notify = vi.fn();
    await installRecommendedTools(notify);
    expect(toolApi.installTool).toHaveBeenCalledTimes(2);
    expect(notify).toHaveBeenLastCalledWith('Recommended tools are installed.', 'success');
  });
});
