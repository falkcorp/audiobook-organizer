// file: web/tests/e2e/version-management.spec.ts
// version: 1.4.0
// guid: 570ee522-c0f2-4d0c-ba5c-b5399cede9a9
// last-edited: 2026-08-09

import { test, expect, type Page } from '@playwright/test';
import {
  generateTestBooks,
  setupMockApi,
} from './utils/test-helpers';

const baseBook = generateTestBooks(1)[0];

const buildBook = (overrides: Record<string, unknown>) => ({
  ...baseBook,
  ...overrides,
});

// Version management is no longer reachable from Book Detail. BookDetail
// renders BookDetailVersionGroup, which is read-only (Bitrate, Duration, File,
// Origin, Path, Sample Rate, Size) — it displays version state but cannot
// change it, which is why this spec's DOM snapshots looked entirely correct.
// The interactive VersionManagement dialog was relocated to the library card's
// overflow menu, where it is a MenuItem (AudiobookCard.tsx:333), not a button.
//
// Whether losing it from Book Detail was intended is a product question, filed
// in todo.d — this helper only reaches the dialog by the route that exists.
const openVersionManager = async (
  page: Page,
  books: Record<string, unknown>[],
  targetIndex = 0
) => {
  await setupMockApi(page, { books });
  await page.goto('/library');
  await page.waitForLoadState('networkidle');

  const title = String(books[targetIndex].title);
  const card = page
    .getByText(title, { exact: true })
    .locator('xpath=ancestor::*[contains(@class,"MuiCard-root")][1]');
  // The overflow IconButton carries no accessible name (AudiobookCard.tsx:183),
  // so it can only be found by the icon MUI stamps inside it.
  await card.locator('button:has([data-testid="MoreVertIcon"])').click();
  await page.getByRole('menuitem', { name: 'Manage Versions' }).click();
};

test.describe('Version Management', () => {
  test.beforeEach(async () => {
    // Setup handled by openVersionManager() which calls setupMockApi()
  });

  test('links two books as versions', async ({ page }) => {
    // Arrange
    const bookA = buildBook({
      id: 'book-a',
      title: 'The Way of Kings',
      author_name: 'Brandon Sanderson',
    });
    const bookB = buildBook({
      id: 'book-b',
      title: 'The Way of Kings (MP3)',
      author_name: 'Brandon Sanderson',
    });
    await openVersionManager(page, [bookA, bookB]);

    // Act
    await page.getByRole('button', { name: 'Link Another Version' }).click();
    await page.getByLabel('Search by title or author').fill('Way of Kings');
    // Scope to the link dialog: this now runs on /library, where the same title
    // is also rendered on the card behind the dialog.
    await page.getByRole('dialog').last().getByText('The Way of Kings (MP3)').click();
    await page.getByRole('button', { name: 'Link Version' }).click();

    // Assert — the newly linked version now shows in the Manage Versions
    // dialog. Scoped, because the same title is also on the card behind it.
    //
    // This test used to continue by closing the dialog, opening a Book Detail
    // "Versions" tab and asserting 'Part of version group with 2 books.'. That
    // tab and that string are both gone; see the removal note below.
    await expect(
      page.getByRole('dialog', { name: 'Manage Versions' })
        .getByText('The Way of Kings (MP3)')
    ).toBeVisible();
  });

  test('sets primary version', async ({ page }) => {
    // Arrange
    const bookA = buildBook({
      id: 'book-a',
      title: 'The Way of Kings',
      author_name: 'Brandon Sanderson',
      version_group_id: 'group-1',
      is_primary_version: true,
    });
    const bookB = buildBook({
      id: 'book-b',
      title: 'The Way of Kings (MP3)',
      author_name: 'Brandon Sanderson',
      version_group_id: 'group-1',
      is_primary_version: false,
    });
    await openVersionManager(page, [bookA, bookB], 1);

    // Act
    await page
      .getByRole('button', { name: 'Set primary for The Way of Kings (MP3)' })
      .click();

    // Assert
    const currentRow = page
      .getByRole('listitem')
      .filter({ hasText: 'The Way of Kings (MP3)' })
      .first();
    await expect(currentRow.getByText('Primary')).toBeVisible();
  });

  test('unlinks version', async ({ page }) => {
    // Arrange
    const bookA = buildBook({
      id: 'book-a',
      title: 'The Way of Kings',
      author_name: 'Brandon Sanderson',
      version_group_id: 'group-1',
      is_primary_version: true,
    });
    const bookB = buildBook({
      id: 'book-b',
      title: 'The Way of Kings (MP3)',
      author_name: 'Brandon Sanderson',
      version_group_id: 'group-1',
      is_primary_version: false,
    });
    await openVersionManager(page, [bookA, bookB]);

    // Act
    const row = page
      .getByRole('listitem')
      .filter({ hasText: 'The Way of Kings (MP3)' })
      .first();
    await row.getByRole('button', { name: 'Unlink' }).click();
    await page.getByRole('button', { name: 'Unlink' }).last().click();

    // Assert
    await expect(page.getByText('No Additional Versions')).toBeVisible();
  });

  // REMOVED 2026-08-09: 'navigates between versions' and 'shows version group
  // information'. Both clicked a Book Detail tab named /Versions/, which no
  // longer exists — BookDetail.tsx:1014-1015 renders only Info and
  // Files & History — and both assert capabilities that are gone rather than
  // relocated:
  //
  //   - Version-to-version navigation. BookDetailVersionGroup.tsx renders no
  //     RouterLink and VersionManagement.tsx has no navigate() call, so clicking
  //     a sibling version no longer takes you to it. The only per-version action
  //     left is "Move to: <title>", which moves FILES between versions — a
  //     different operation with a destructive outcome, not navigation.
  //   - The group summary. 'Part of version group with 3 books.' and '(Current)'
  //     appear nowhere in web/src. All that survives is a bare
  //     "Version Group Linked" chip (BookDetailHeader.tsx:172) with no count and
  //     no indication of which version you are looking at.
  //
  // See todo.d/20260809-version-group-navigation-and-summary.md.

  test('prevents circular version links', async ({ page }) => {
    // Arrange
    const groupId = 'group-4';
    const bookA = buildBook({
      id: 'book-a',
      title: 'The Way of Kings',
      author_name: 'Brandon Sanderson',
      version_group_id: groupId,
      is_primary_version: true,
    });
    const bookB = buildBook({
      id: 'book-b',
      title: 'The Way of Kings (MP3)',
      author_name: 'Brandon Sanderson',
      version_group_id: groupId,
      is_primary_version: false,
    });
    const bookC = buildBook({
      id: 'book-c',
      title: 'The Way of Kings (FLAC)',
      author_name: 'Brandon Sanderson',
      version_group_id: groupId,
      is_primary_version: false,
    });
    await openVersionManager(page, [bookA, bookB, bookC]);

    // Act
    await page.getByRole('button', { name: 'Link Another Version' }).click();
    await page.getByLabel('Search by title or author').fill('FLAC');
    await page.locator('[role="button"]').filter({ hasText: 'The Way of Kings (FLAC)' }).click();
    await page.getByRole('button', { name: 'Link Version' }).click();

    // Assert
    await expect(
      page.getByText('Cannot create circular version links')
    ).toBeVisible();
  });
});
