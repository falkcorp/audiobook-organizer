// file: web/src/modules/books/pages/booksSetup.isRequired.test.ts
// version: 0.1.0
// guid: 98fdad54-30bf-4015-ad48-a6973b6af7bf
// last-edited: 2026-11-21

// F9-04 slice 5: books' setup is required exactly when !config.root_dir && !config.setup_complete
// (a blank root directory counts as unset), and it names the five steps the wizard always had.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../../services/api';
import { booksSetup, isBooksSetupRequired } from './booksSetup';

vi.mock('../../../services/api', () => ({ getConfig: vi.fn() }));

beforeEach(() => {
  vi.mocked(api.getConfig).mockReset();
});

describe('booksSetup.isRequired', () => {
  it.each([
    [{ root_dir: '', setup_complete: false }, true],
    [{}, true],
    [{ root_dir: '   ' }, true],
    [{ root_dir: '   ', setup_complete: false }, true],
    [{ root_dir: '/library/gotham', setup_complete: false }, false],
    [{ root_dir: '/library/gotham' }, false],
    [{ root_dir: '', setup_complete: true }, false],
    [{ setup_complete: true }, false],
    [{ root_dir: '/library/gotham', setup_complete: true }, false],
  ])('config %j -> required %s', async (config, required) => {
    vi.mocked(api.getConfig).mockResolvedValue(config as never);
    expect(await isBooksSetupRequired()).toBe(required);
    expect(await booksSetup.isRequired()).toBe(required);
  });

  it('rejects when the config cannot be read, so the caller can fall back', async () => {
    vi.mocked(api.getConfig).mockImplementation(async () => {
      throw new Error('offline');
    });
    await expect(booksSetup.isRequired()).rejects.toThrow('offline');
  });

  it('lists the five steps in the wizard order', () => {
    expect(booksSetup.steps.map((s) => [s.id, s.title])).toEqual([
      ['library-path', 'Library Path'],
      ['tools', 'Tools'],
      ['ai', 'AI Setup (Optional)'],
      ['itunes', 'iTunes Library'],
      ['import-folders', 'Import Folders'],
    ]);
  });
});
