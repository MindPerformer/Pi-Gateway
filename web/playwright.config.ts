import {defineConfig, devices} from '@playwright/test'

// The real Vite application runs over HTTP. All administrator endpoints are
// intercepted by per-test fixtures; no real credentials or captures are used.
export default defineConfig({
    testDir: './e2e',
    globalSetup: './e2e/catalog.setup.ts',
    fullyParallel: true,
    forbidOnly: Boolean(process.env.CI),
    retries: process.env.CI ? 1 : 0,
    workers: process.env.CI ? 2 : undefined,
    timeout: 45_000,
    expect: {timeout: 8_000},
    outputDir: './node_modules/.cache/playwright/test-results',
    reporter: [
        ['list'],
        ['html', {outputFolder: './node_modules/.cache/playwright/report', open: 'never'}],
    ],
    use: {
        baseURL: 'http://127.0.0.1:5274',
        viewport: {width: 1440, height: 1050},
        locale: 'zh-CN',
        // An installed Chrome/Edge may be used locally when the CDN is unavailable.
        // CI leaves this unset and runs the Chromium revision installed above.
        channel: process.env.PLAYWRIGHT_CHANNEL,
        trace: 'retain-on-failure',
        screenshot: 'only-on-failure',
        video: 'retain-on-failure',
        serviceWorkers: 'block',
    },
    projects: [{name: 'chromium', use: {...devices['Desktop Chrome'], viewport: {width: 1440, height: 1050}}}],
    webServer: {
        command: 'npm run dev -- --port 5274 --strictPort',
        url: 'http://127.0.0.1:5274/login',
        reuseExistingServer: !process.env.CI,
        timeout: 120_000,
    },
})
