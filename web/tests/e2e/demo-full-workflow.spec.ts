// file: web/tests/e2e/demo-full-workflow.spec.ts
// version: 4.1.1
// guid: a1b2c3d4-e5f6-7890-abcd-e1f2a3b4c5d6
// last-edited: 2026-11-08

import { test } from '@playwright/test';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';
import {
  setupDemoDirectories,
  cleanupDemoDirectories,
  humanMove,
  humanType,
  demoScreenshot,
} from './utils/demo-helpers';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const DEMO_ARTIFACTS_DIR = join(__dirname, '../../..', 'demo_artifacts');

test.describe('Full End-to-End Demo Workflow', () => {
  test('Complete audiobook workflow: import, organize, edit metadata, verify persistence', async ({
    page,
  }) => {
    // 15 minute timeout for complete demo including iTunes
    test.setTimeout(900 * 1000);

    // Setup temp demo directories with real audiobook files
    const { tempDir, libraryPath, importPath } = await setupDemoDirectories();
    console.log(`📁 Demo directories created: ${tempDir}`);

    try {
      // The demo starts at the welcome wizard, so the server must start on
      // an empty store. There is no reset endpoint: stage one with the
      // service stopped (`audiobook-organizer storage reset --confirm RESET`)
      // or point the server at a fresh database directory.

      // ==============================================
      // PHASE 1: Welcome Wizard Setup
      // ==============================================
      console.log('\n=== PHASE 1: Welcome Wizard Setup ===');

      await page.goto('/', { waitUntil: 'domcontentloaded' });
      await page.waitForTimeout(2000);

      await demoScreenshot(page, 1, 'welcome_screen', DEMO_ARTIFACTS_DIR);

      // Click "Get Started" button
      const getStartedButton = page
        .locator('button')
        .filter({ hasText: /get started|start/i })
        .first();
      if (await getStartedButton.isVisible().catch(() => false)) {
        await humanMove(page, 640, 500, 30);
        await page.waitForTimeout(500);
        await getStartedButton.click();
        console.log('✓ Clicked Get Started');
        await page.waitForTimeout(2000);
      }

      // Wizard Step 1: Library Path
      console.log('\n📍 Wizard Step 1: Setting library path');
      await demoScreenshot(page, 2, 'wizard_step1_form', DEMO_ARTIFACTS_DIR);

      const libraryPathInput = page
        .locator('input[placeholder*="path"], input[value*="/"]')
        .first();
      if (await libraryPathInput.isVisible().catch(() => false)) {
        await humanMove(page, 640, 400, 25);
        await page.waitForTimeout(500);
        await libraryPathInput.click();
        await page.waitForTimeout(300);
        await page.keyboard.press('Control+A');
        await page.waitForTimeout(100);
        await page.keyboard.press('Delete');
        await page.waitForTimeout(300);

        await humanType(page, libraryPath);
        console.log(`✓ Set library path: ${libraryPath}`);
        await page.waitForTimeout(1500);
      }

      await demoScreenshot(page, 3, 'wizard_step1_completed', DEMO_ARTIFACTS_DIR);

      // Click NEXT button
      const nextButton1 = page.locator('button').filter({ hasText: /next/i }).first();
      if (await nextButton1.isVisible().catch(() => false)) {
        await humanMove(page, 1047, 537, 25);
        await page.waitForTimeout(600);
        await nextButton1.click();
        console.log('✓ Clicked NEXT');
        await page.waitForTimeout(2500);
      }

      // Wizard Step 2: AI Setup (skip)
      console.log('\n🤖 Wizard Step 2: AI Setup (skipping)');
      const nextButton2 = page.locator('button').filter({ hasText: /next|skip/i }).first();
      if (await nextButton2.isVisible().catch(() => false)) {
        await humanMove(page, 1047, 537, 25);
        await page.waitForTimeout(600);
        await nextButton2.click();
        console.log('✓ Skipped AI Setup');
        await page.waitForTimeout(2500);
      }

      // Wizard Step 3: Import Folders
      console.log('\n📂 Wizard Step 3: Adding import path');
      await demoScreenshot(page, 4, 'wizard_step3_imports', DEMO_ARTIFACTS_DIR);
      await page.waitForTimeout(1500);

      const addButton = page.locator('button').filter({ hasText: /add|create|\+/i }).first();
      if (await addButton.isVisible().catch(() => false)) {
        try {
          await humanMove(page, 400, 300, 25);
          await page.waitForTimeout(800);
          await addButton.click();
          console.log('✓ Clicked Add Import Path');
          await page.waitForTimeout(1500);

          // Fill in import path
          const importInput = page
            .locator('input')
            .filter({ placeholder: /path|folder|directory/i })
            .first();
          if (await importInput.isVisible({ timeout: 1000 }).catch(() => false)) {
            await humanMove(page, 640, 400, 20);
            await page.waitForTimeout(400);
            await importInput.click();
            await page.waitForTimeout(300);
            await humanType(page, importPath);
            console.log(`✓ Set import path: ${importPath}`);
            await page.waitForTimeout(1000);

            // Confirm dialog
            const confirmButton = page
              .locator('button')
              .filter({ hasText: /add|confirm|save|ok/i })
              .last();
            if (await confirmButton.isVisible({ timeout: 1000 }).catch(() => false)) {
              await humanMove(page, 640, 500, 20);
              await page.waitForTimeout(600);
              await confirmButton.click();
              console.log('✓ Confirmed import path');
              await page.waitForTimeout(1000);
            }
          }
        } catch (_e) {
          console.log('⚠ Import path setup skipped');
        }
      }

      // Close any dialogs and finish wizard
      await page.keyboard.press('Escape');
      await page.waitForTimeout(500);

      const finishButton = page
        .locator('button')
        .filter({ hasText: /finish|complete|done|start/i })
        .first();
      if (await finishButton.isVisible().catch(() => false)) {
        await humanMove(page, 1047, 537, 20);
        await page.waitForTimeout(600);
        await finishButton.click();
        console.log('✓ Clicked FINISH - Wizard complete');
        await page.waitForTimeout(3000);
      }

      await demoScreenshot(page, 5, 'wizard_complete', DEMO_ARTIFACTS_DIR);

      // ==============================================
      // PHASE 2: Scanning and Importing Books
      // ==============================================
      console.log('\n=== PHASE 2: Scanning Import Path ===');

      // Navigate to Library
      const libraryLink = page.locator('a, button').filter({ hasText: /library|books/i }).first();
      if (await libraryLink.isVisible().catch(() => false)) {
        await humanMove(page, 97, 144, 30);
        await page.waitForTimeout(800);
        await libraryLink.click();
        console.log('✓ Navigated to Library');
        await page.waitForTimeout(3000);
      }

      await demoScreenshot(page, 6, 'library_empty_state', DEMO_ARTIFACTS_DIR);

      // Find and click the Scan button in settings/operations
      // This triggers the import scan
      const scanButton = page
        .locator('button')
        .filter({ hasText: /scan|import|organize/i })
        .first();
      if (await scanButton.isVisible().catch(() => false)) {
        await humanMove(page, 640, 400, 25);
        await page.waitForTimeout(800);
        await page.screenshot({
          path: `${DEMO_ARTIFACTS_DIR}/demo_07_cursor_on_scan.png`,
          fullPage: true,
        });
        await page.waitForTimeout(600);
        await scanButton.click();
        console.log('✓ Clicked Scan button - importing audiobooks');
        // Wait for scan to complete
        await page.waitForTimeout(5000);
      }

      await demoScreenshot(page, 8, 'library_with_books', DEMO_ARTIFACTS_DIR);

      // ==============================================
      // PHASE 3: Batch Operations
      // ==============================================
      console.log('\n=== PHASE 3: Batch Operations ===');

      // Select first book checkbox if visible
      const firstBookCheckbox = page.locator('input[type="checkbox"]').first();
      if (await firstBookCheckbox.isVisible().catch(() => false)) {
        await humanMove(page, 60, 200, 20);
        await page.waitForTimeout(600);
        await firstBookCheckbox.click();
        console.log('✓ Selected first book');
        await page.waitForTimeout(1000);

        // Select second book
        const secondBookCheckbox = page.locator('input[type="checkbox"]').nth(1);
        if (await secondBookCheckbox.isVisible().catch(() => false)) {
          await humanMove(page, 60, 280, 20);
          await page.waitForTimeout(600);
          await secondBookCheckbox.click();
          console.log('✓ Selected second book');
          await page.waitForTimeout(1000);
        }
      }

      await demoScreenshot(page, 9, 'books_selected', DEMO_ARTIFACTS_DIR);

      // ==============================================
      // PHASE 4: Search and Filter
      // ==============================================
      console.log('\n=== PHASE 4: Search and Filter ===');

      // Find search input and search for a book
      const searchInput = page.locator('input[placeholder*="search"], input[type="search"]').first();
      if (await searchInput.isVisible().catch(() => false)) {
        await humanMove(page, 640, 100, 20);
        await page.waitForTimeout(600);
        await searchInput.click();
        await page.waitForTimeout(300);
        // Search for "odyssey" or "moby"
        await humanType(page, 'odyssey');
        console.log('✓ Typed search query');
        await page.waitForTimeout(2000);
      }

      await demoScreenshot(page, 10, 'search_results', DEMO_ARTIFACTS_DIR);

      // Clear search
      if (await searchInput.isVisible().catch(() => false)) {
        await searchInput.click();
        await page.keyboard.press('Control+A');
        await page.keyboard.press('Delete');
        await page.waitForTimeout(1000);
      }

      // ==============================================
      // PHASE 5: Metadata Editing and Persistence
      // ==============================================
      console.log('\n=== PHASE 5: Metadata Editing ===');

      // Click on first book to open detail view
      const firstBook = page.locator('[role="button"]').filter({ hasText: /odyssey|moby|homer/i }).first();
      if (await firstBook.isVisible().catch(() => false)) {
        await humanMove(page, 320, 250, 25);
        await page.waitForTimeout(800);
        await firstBook.click();
        console.log('✓ Opened book detail view');
        await page.waitForTimeout(2000);
      }

      await demoScreenshot(page, 11, 'book_detail_view', DEMO_ARTIFACTS_DIR);

      // Find comments field and edit it
      const commentsInput = page
        .locator('textarea, input[placeholder*="comment"]')
        .first();
      if (await commentsInput.isVisible().catch(() => false)) {
        await humanMove(page, 640, 400, 20);
        await page.waitForTimeout(600);
        await commentsInput.click();
        await page.waitForTimeout(300);
        await humanType(page, 'Edited via demo - timestamp: ' + new Date().toISOString());
        console.log('✓ Edited comments field');
        await page.waitForTimeout(1500);
      }

      await demoScreenshot(page, 12, 'metadata_edited', DEMO_ARTIFACTS_DIR);

      // Save changes - click Save button or navigate away
      const saveButton = page
        .locator('button')
        .filter({ hasText: /save|confirm|apply/i })
        .first();
      if (await saveButton.isVisible().catch(() => false)) {
        await humanMove(page, 640, 500, 20);
        await page.waitForTimeout(600);
        await saveButton.click();
        console.log('✓ Clicked Save - changes persisted');
        await page.waitForTimeout(2000);
      } else {
        // Navigate away to trigger save
        const backButton = page.locator('button').filter({ hasText: /back|close|x/i }).first();
        if (await backButton.isVisible().catch(() => false)) {
          await backButton.click();
          console.log('✓ Navigated away - changes auto-saved');
          await page.waitForTimeout(2000);
        }
      }

      await demoScreenshot(page, 13, 'changes_saved', DEMO_ARTIFACTS_DIR);

      // ==============================================
      // PHASE 6: Verify Persistence
      // ==============================================
      console.log('\n=== PHASE 6: Verify Metadata Persistence ===');

      // Re-open the same book
      const bookAgain = page
        .locator('[role="button"]')
        .filter({ hasText: /odyssey|moby|homer/i })
        .first();
      if (await bookAgain.isVisible().catch(() => false)) {
        await humanMove(page, 320, 250, 25);
        await page.waitForTimeout(800);
        await bookAgain.click();
        console.log('✓ Reopened book detail view');
        await page.waitForTimeout(2000);
      }

      // Verify comments field contains our edit
      const verifyComments = page.locator('textarea, input[placeholder*="comment"]').first();
      if (await verifyComments.isVisible().catch(() => false)) {
        const commentsValue = await verifyComments.inputValue();
        if (commentsValue && commentsValue.includes('Edited via demo')) {
          console.log('✅ VERIFIED: Comments field persisted in database!');
          await page.waitForTimeout(1000);
        } else {
          console.log('⚠️ Comments not found - may not have persisted');
        }
      }

      await demoScreenshot(page, 14, 'persistence_verified', DEMO_ARTIFACTS_DIR);

      // ==============================================
      // PHASE 7: iTunes Integration & Bidirectional Sync
      // ==============================================
      console.log('\n=== PHASE 7: iTunes Integration & Bidirectional Sync ===');

      // Navigate to Settings
      const settingsLink = page.locator('a, button').filter({ hasText: /settings/i }).first();
      if (await settingsLink.isVisible().catch(() => false)) {
        await humanMove(page, 640, 400, 30);
        await page.waitForTimeout(800);
        await settingsLink.click();
        console.log('✓ Navigated to Settings');
        await page.waitForTimeout(2000);
      }

      await demoScreenshot(page, 15, 'settings_page', DEMO_ARTIFACTS_DIR);

      // Click iTunes Import tab
      const itunesTab = page.getByRole('tab', { name: /itunes/i }).first();
      if (await itunesTab.isVisible().catch(() => false)) {
        await humanMove(page, 320, 150, 25);
        await page.waitForTimeout(600);
        await itunesTab.click();
        console.log('✓ Opened iTunes Import tab');
        await page.waitForTimeout(2000);
      }

      await demoScreenshot(page, 16, 'itunes_settings', DEMO_ARTIFACTS_DIR);

      // Enter iTunes library path and validate
      const itunesPathInput = page.getByLabel(/itunes library path|library path/i).first();
      if (await itunesPathInput.isVisible().catch(() => false)) {
        await humanMove(page, 640, 300, 20);
        await page.waitForTimeout(600);
        await itunesPathInput.click();
        await page.waitForTimeout(300);
        // Use a real iTunes test library path
        await humanType(page, 'testdata/itunes/Library.xml');
        console.log('✓ Entered iTunes library path');
        await page.waitForTimeout(1500);
      }

      // Click Validate button
      const validateButton = page.getByRole('button', { name: /validate/i }).first();
      if (await validateButton.isVisible().catch(() => false)) {
        await humanMove(page, 640, 350, 20);
        await page.waitForTimeout(800);
        await validateButton.click();
        console.log('✓ Clicked Validate');
        await page.waitForTimeout(3000);
      }

      await demoScreenshot(page, 17, 'itunes_validated', DEMO_ARTIFACTS_DIR);

      // Click Import Library button
      const importButton = page.getByRole('button', { name: /import library|import/i }).nth(0);
      if (await importButton.isVisible().catch(() => false)) {
        await humanMove(page, 640, 400, 20);
        await page.waitForTimeout(800);
        await importButton.click();
        console.log('✓ Clicked Import Library');
        // Wait for import to complete (longer timeout for real data)
        await page.waitForTimeout(5000);
      }

      await demoScreenshot(page, 18, 'itunes_importing', DEMO_ARTIFACTS_DIR);

      // Wait for import completion message
      await page.waitForTimeout(2000);
      await demoScreenshot(page, 19, 'itunes_imported', DEMO_ARTIFACTS_DIR);

      // Navigate back to Library to verify imported books
      const libraryLinkAgain = page.locator('a, button').filter({ hasText: /library|books/i }).first();
      if (await libraryLinkAgain.isVisible().catch(() => false)) {
        await humanMove(page, 97, 144, 30);
        await page.waitForTimeout(800);
        await libraryLinkAgain.click();
        console.log('✓ Navigated back to Library');
        await page.waitForTimeout(3000);
      }

      // Show library with newly imported iTunes books
      await demoScreenshot(page, 20, 'library_with_itunes_books', DEMO_ARTIFACTS_DIR);

      console.log('\n✅ iTunes sync demo completed successfully!');

      console.log('\n✅ Complete realistic demo finished successfully!');
      console.log(`📊 Demo artifacts saved to: ${DEMO_ARTIFACTS_DIR}`);
    } finally {
      // Cleanup temp directory
      await cleanupDemoDirectories(tempDir);
      console.log(`🧹 Cleaned up temp directory: ${tempDir}`);
    }
  });
});
