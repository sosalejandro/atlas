// @testreg server.e2e-navigation
import { test, expect, Page } from '@playwright/test';

// ─── Helpers ─────────────────────────────────────────────────────────────────

/**
 * Counts full-document navigations (i.e. real page loads, not htmx swaps).
 * We attach a counter to window that increments on DOMContentLoaded.
 * An htmx swap does NOT fire DOMContentLoaded, so the counter stays the same.
 */
async function trackFullLoads(page: Page): Promise<() => Promise<number>> {
    await page.evaluate(() => {
        (window as any).__fullLoads = 0;
        document.addEventListener('DOMContentLoaded', () => {
            (window as any).__fullLoads++;
        });
    });
    return () => page.evaluate(() => (window as any).__fullLoads as number);
}

/**
 * Returns the text content of #page-content.
 */
async function pageContent(page: Page): Promise<string> {
    return page.locator('#page-content').innerText();
}

// ─── Initial load ─────────────────────────────────────────────────────────────

test('initial load renders full shell', async ({ page }) => {
    // Use domcontentloaded to avoid waiting for external CDN resources (fonts, icons).
    await page.goto('/', { waitUntil: 'domcontentloaded' });

    // Shell landmarks are present.
    await expect(page.locator('#page-content')).toBeVisible();
    await expect(page.locator('#status-bar')).toBeVisible();
    await expect(page.locator('#modal-container')).toBeAttached();

    // Overview content is inside #page-content (no h1 "Overview" — check actual content).
    await expect(page.locator('#page-content')).toContainText('Health by Priority');

    // URL is correct.
    expect(page.url()).toContain('/');
});

test('status bar shows feature counts', async ({ page }) => {
    await page.goto('/');
    const statusBar = page.locator('#status-bar');
    // Should contain a number (total features).
    await expect(statusBar).toContainText(/\d+\s+features/);
});

// ─── Sidebar navigation (htmx swaps) ─────────────────────────────────────────

test('nav: Features link swaps content without full reload', async ({ page }) => {
    await page.goto('/');
    const getLoads = await trackFullLoads(page);
    const loadsBefore = await getLoads();

    // Click the Features nav link.
    await page.getByRole('link', { name: 'Features' }).click();

    // Content changed — Features page is visible.
    await expect(page.locator('#page-content')).toContainText('Features Analysis');

    // URL updated via hx-push-url.
    await expect(page).toHaveURL(/\/features/);

    // No full page reload happened.
    const loadsAfter = await getLoads();
    expect(loadsAfter).toBe(loadsBefore);
});

test('nav: Graph link swaps content without full reload', async ({ page }) => {
    await page.goto('/');
    const getLoads = await trackFullLoads(page);
    const loadsBefore = await getLoads();

    await page.getByRole('link', { name: 'Graph' }).click();
    await expect(page.locator('#page-content')).toContainText('Dependency Graph');
    await expect(page).toHaveURL(/\/graph/);

    expect(await getLoads()).toBe(loadsBefore);
});

test('nav: Sprint link swaps content without full reload', async ({ page }) => {
    await page.goto('/');
    const getLoads = await trackFullLoads(page);
    const loadsBefore = await getLoads();

    // Scope to aside to avoid matching the overview "→ View all sprint priorities" link.
    await page.locator('aside a[href="/sprint"]').click();
    await expect(page.locator('#page-content')).toContainText('Sprint Planning');
    await expect(page).toHaveURL(/\/sprint/);

    expect(await getLoads()).toBe(loadsBefore);
});

test('nav: Diff link swaps content without full reload', async ({ page }) => {
    await page.goto('/');
    const getLoads = await trackFullLoads(page);
    const loadsBefore = await getLoads();

    await page.getByRole('link', { name: 'Diff' }).click();
    await expect(page.locator('#page-content')).toContainText('Progress Tracking');
    await expect(page).toHaveURL(/\/diff/);

    expect(await getLoads()).toBe(loadsBefore);
});

test('nav: Diagnose link swaps content without full reload', async ({ page }) => {
    await page.goto('/');
    const getLoads = await trackFullLoads(page);
    const loadsBefore = await getLoads();

    await page.getByRole('link', { name: 'Diagnose' }).click();
    await expect(page.locator('#page-content')).toContainText('Symptom');
    await expect(page).toHaveURL(/\/diagnose/);

    expect(await getLoads()).toBe(loadsBefore);
});

test('nav: Metrics link swaps content without full reload', async ({ page }) => {
    await page.goto('/');
    const getLoads = await trackFullLoads(page);
    const loadsBefore = await getLoads();

    await page.getByRole('link', { name: 'Metrics' }).click();
    await expect(page.locator('#page-content')).toContainText('Quality Signals');
    await expect(page).toHaveURL(/\/metrics/);

    expect(await getLoads()).toBe(loadsBefore);
});

test('nav: Contract link swaps content without full reload', async ({ page }) => {
    await page.goto('/');
    const getLoads = await trackFullLoads(page);
    const loadsBefore = await getLoads();

    // Scope to aside to get the sidebar nav link specifically.
    await page.locator('aside a[href="/contract"]').click();
    // Contract partial shows feature selector + "layers detected" badge.
    await expect(page.locator('#page-content')).toContainText('layers detected');
    await expect(page).toHaveURL(/\/contract/);

    expect(await getLoads()).toBe(loadsBefore);
});

// ─── Browser back/forward after htmx navigation ───────────────────────────────

test('browser back restores previous content and URL', async ({ page }) => {
    await page.goto('/');

    // Navigate: Overview → Features → Graph
    await page.getByRole('link', { name: 'Features' }).click();
    await expect(page.locator('#page-content')).toContainText('Features Analysis');

    await page.getByRole('link', { name: 'Graph' }).click();
    await expect(page.locator('#page-content')).toContainText('Dependency Graph');
    await expect(page).toHaveURL(/\/graph/);

    // Back → Features
    await page.goBack();
    await expect(page).toHaveURL(/\/features/);
    await expect(page.locator('#page-content')).toContainText('Features Analysis');

    // Back → Overview
    await page.goBack();
    await expect(page).toHaveURL(/\/$/);
    await expect(page.locator('#page-content')).toContainText('Health by Priority');
});

test('browser forward works after going back', async ({ page }) => {
    await page.goto('/');

    await page.locator('aside a[href="/sprint"]').click();
    await expect(page).toHaveURL(/\/sprint/);

    await page.goBack();
    await expect(page).toHaveURL(/\/$/);

    await page.goForward();
    await expect(page).toHaveURL(/\/sprint/);
    await expect(page.locator('#page-content')).toContainText('Sprint Planning');
});

// ─── Direct URL load (first-visit) ────────────────────────────────────────────

test('direct load of /features renders full shell', async ({ page }) => {
    await page.goto('/features');

    await expect(page.locator('#page-content')).toContainText('Features Analysis');
    await expect(page.locator('#status-bar')).toBeVisible();
    await expect(page).toHaveURL(/\/features/);
});

test('direct load of /graph renders full shell', async ({ page }) => {
    await page.goto('/graph');
    await expect(page.locator('#page-content')).toContainText('Dependency Graph');
    await expect(page.locator('#status-bar')).toBeVisible();
});

test('direct load of /diff renders full shell', async ({ page }) => {
    await page.goto('/diff');
    await expect(page.locator('#page-content')).toContainText('Progress Tracking');
    await expect(page.locator('#status-bar')).toBeVisible();
});

// ─── Scan modal lifecycle ─────────────────────────────────────────────────────

test('scan button opens modal overlay', async ({ page }) => {
    await page.goto('/');

    // Modal should not exist yet.
    await expect(page.locator('#scan-modal-overlay')).not.toBeAttached();

    // Click Scan button.
    await page.locator('#scan-btn').click();

    // Modal appears.
    await expect(page.locator('#scan-modal-overlay')).toBeVisible();
    await expect(page.locator('#scan-modal-overlay')).toContainText('Scan & Import');
    await expect(page.locator('#scan-modal-overlay')).toContainText('Run Scan');
});

test('scan modal close button dismisses overlay', async ({ page }) => {
    await page.goto('/');
    await page.locator('#scan-btn').click();
    await expect(page.locator('#scan-modal-overlay')).toBeVisible();

    // Close via X button.
    await page.locator('#scan-modal-overlay').getByRole('button', { name: /close/i }).click();
    await expect(page.locator('#scan-modal-overlay')).not.toBeAttached();
});

test('scan modal closes on backdrop click', async ({ page }) => {
    await page.goto('/');
    await page.locator('#scan-btn').click();
    await expect(page.locator('#scan-modal-overlay')).toBeVisible();

    // Click on the backdrop (the overlay div itself, not the modal card).
    await page.locator('#scan-modal-overlay').click({ position: { x: 10, y: 10 } });
    await expect(page.locator('#scan-modal-overlay')).not.toBeAttached();
});

// ─── Feature detail panel (slide-over) ────────────────────────────────────────

test('clicking a feature row opens detail panel', async ({ page }) => {
    await page.goto('/features');

    // Panel should not exist yet.
    await expect(page.locator('#feature-detail-panel')).not.toBeAttached();

    // Click the first feature row.
    await page.locator('tbody tr').first().click();

    // Panel slides in.
    await expect(page.locator('#feature-detail-panel')).toBeVisible();
    await expect(page.locator('#feature-detail-panel')).toContainText('health');
});

test('detail panel close button removes panel', async ({ page }) => {
    await page.goto('/features');
    await page.locator('tbody tr').first().click();
    await expect(page.locator('#feature-detail-panel')).toBeVisible();

    // Close button inside the panel.
    await page.locator('#feature-detail-panel button').filter({ hasText: '' }).first().click();
    await expect(page.locator('#feature-detail-panel')).not.toBeAttached();
});

test('detail panel does not open when clicking action links', async ({ page }) => {
    await page.goto('/features');

    // Hover a row to reveal action links, then click Contract (stops propagation).
    const row = page.locator('tbody tr').first();
    await row.hover();
    const contractLink = row.locator('a', { hasText: 'Contract' });

    if (await contractLink.isVisible()) {
        await contractLink.click();
        // Should navigate to contract page, not open detail panel.
        await expect(page.locator('#page-content')).toContainText('Contract');
        await expect(page.locator('#feature-detail-panel')).not.toBeAttached();
    } else {
        test.skip(); // link not visible in this data set
    }
});

// ─── Features filter ──────────────────────────────────────────────────────────

test('search filter narrows feature rows without page reload', async ({ page }) => {
    await page.goto('/features');
    const getLoads = await trackFullLoads(page);
    const loadsBefore = await getLoads();

    const input = page.getByPlaceholder('Filter features...');
    await input.pressSequentially('auth', { delay: 50 });

    // Wait for debounced htmx request to complete.
    await page.waitForResponse(resp => resp.url().includes('/pages/features') && resp.status() === 200);

    const rows = page.locator('#feature-table-body tr');
    const rowCount = await rows.count();
    for (let i = 0; i < rowCount; i++) {
        const text = await rows.nth(i).innerText();
        // Each visible row must contain "auth" (or be the empty-state row).
        if (!text.includes('No features match')) {
            expect(text.toLowerCase()).toContain('auth');
        }
    }

    // No full reload.
    expect(await getLoads()).toBe(loadsBefore);
});

test('priority filter shows only matching priority rows', async ({ page }) => {
    await page.goto('/features');

    await page.selectOption('select[name="priority"]', 'critical');
    await page.waitForResponse(resp => resp.url().includes('/pages/features') && resp.status() === 200);

    const rows = page.locator('#feature-table-body tr');
    const count = await rows.count();
    for (let i = 0; i < count; i++) {
        const text = await rows.nth(i).innerText();
        if (!text.includes('No features match')) {
            expect(text.toLowerCase()).toContain('critical');
        }
    }
});

// ─── Graph feature selector ───────────────────────────────────────────────────

test('graph feature selector swaps trace content without page reload', async ({ page }) => {
    await page.goto('/graph');
    const getLoads = await trackFullLoads(page);
    const loadsBefore = await getLoads();

    const select = page.locator('select[name="feature"]');
    const options = await select.locator('option').allTextContents();

    if (options.length > 1) {
        // Select second option.
        await select.selectOption({ index: 1 });
        await page.waitForResponse(resp => resp.url().includes('/graph') && resp.status() === 200);

        // URL reflects selected feature.
        await expect(page).toHaveURL(/feature=/);

        // No full reload.
        expect(await getLoads()).toBe(loadsBefore);
    } else {
        test.skip(); // only one feature in registry
    }
});

// ─── Sprint row → Features navigation ────────────────────────────────────────

test('sprint row click navigates to features page', async ({ page }) => {
    await page.goto('/sprint');
    const getLoads = await trackFullLoads(page);
    const loadsBefore = await getLoads();

    const firstRow = page.locator('tbody tr').first();
    const count = await page.locator('tbody tr').count();

    if (count > 0 && !(await firstRow.innerText()).includes('All features')) {
        await firstRow.click();
        await expect(page).toHaveURL(/\/features/);
        await expect(page.locator('#page-content')).toContainText('Features Analysis');
        expect(await getLoads()).toBe(loadsBefore);
    } else {
        test.skip(); // no sprint items
    }
});

// ─── Diff snapshot workflow ───────────────────────────────────────────────────

test('diff page: save snapshot updates snapshot list', async ({ page }) => {
    await page.goto('/diff');

    const snapName = `e2e-test-${Date.now()}`;
    await page.fill('input[name="name"]', snapName);
    await page.getByRole('button', { name: /Save Snapshot/i }).click();

    // Page content refreshes with new snapshot.
    await expect(page.locator('#page-content')).toContainText(snapName);
});

test('diff page: vs current link loads comparison results', async ({ page }) => {
    await page.goto('/diff');

    // Save a snapshot first so there's something to compare.
    const snapName = `e2e-compare-${Date.now()}`;
    await page.fill('input[name="name"]', snapName);
    await page.getByRole('button', { name: /Save Snapshot/i }).click();
    await expect(page.locator('#page-content')).toContainText(snapName);

    // Click "vs current" for this snapshot.
    const vsLink = page.locator(`tr:has-text("${snapName}") a:has-text("vs current")`);
    await vsLink.click();

    // Wait for the htmx compare request to complete (result may be empty when baseline = current).
    await page.waitForResponse(resp => resp.url().includes('/api/diff/compare') && resp.status() === 200);
    // The result area is present and the request completed without error.
    await expect(page.locator('#diff-result-area')).toBeAttached();
});
