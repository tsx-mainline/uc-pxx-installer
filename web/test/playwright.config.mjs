// SPDX-License-Identifier: GPL-2.0-or-later
import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "e2e",
  fullyParallel: true,
  forbidOnly: true,
  retries: 0,
  workers: 4,
  reporter: [["list"], ["json", { outputFile: "results/e2e.json" }]],
  outputDir: "results/artifacts",
  use: { baseURL: "http://127.0.0.1:8137", headless: true, acceptDownloads: true, serviceWorkers: "block" },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
  webServer: { command: "node serve.mjs", url: "http://127.0.0.1:8137/index.html", reuseExistingServer: false, timeout: 20000 },
});
