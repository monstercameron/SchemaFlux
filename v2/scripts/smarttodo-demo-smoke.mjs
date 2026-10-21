import fs from "node:fs/promises";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const { chromium } = require("playwright");

const baseUrl = process.env.SMARTTODO_DEMO_URL || "http://127.0.0.1:4173/";
const outputDir = process.env.SMARTTODO_SMOKE_OUTPUT || "artifacts/smarttodo-demo-smoke";
const apiKey = process.env.SMARTTODO_SMOKE_API_KEY || "sk-demo-smoke";
const smokeUrl = new URL(baseUrl);
smokeUrl.searchParams.set("smoke", "1");

await fs.mkdir(outputDir, { recursive: true });

const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
const logs = [];

page.on("console", (msg) => logs.push(`[console:${msg.type()}] ${msg.text()}`));
page.on("pageerror", (err) => logs.push(`[pageerror] ${err.message}`));

try {
  const waitForTerminal = (text) => page.waitForFunction(
    (expected) => window.smarttodoSmokeText?.().includes(expected),
    text,
    { timeout: 15_000 },
  );

  await page.goto(smokeUrl.toString(), { waitUntil: "networkidle" });
  await page.waitForSelector("#wasm-status");
  await page.waitForFunction(() => document.querySelector("#wasm-status")?.textContent?.includes("Ready"));

  await page.fill("#api-key-input", apiKey);
  await page.click("#connect-key");
  await page.waitForFunction(() => document.querySelector("#key-gate")?.classList.contains("hidden"));

  await page.locator("#terminal-root").click();
  await waitForTerminal("Press Enter to continue.");
  await page.keyboard.press("Enter");
  await waitForTerminal("Enter your name to personalize the board.");
  await page.keyboard.insertText("CI User");
  await waitForTerminal("CI User");
  await page.keyboard.press("Enter");
  await waitForTerminal("Name the board for CI User.");
  await page.keyboard.insertText("CI Board");
  await waitForTerminal("CI Board");
  await page.keyboard.press("Enter");
  await waitForTerminal("Task queue");
  await waitForTerminal("CI Board | CI User");

  const sawBoot = logs.some((line) => line.includes("[smarttodo-go] boot"));
  const sawConnect = logs.some((line) => line.includes("[smarttodo-go] connect TUI connected"));
  if (!sawBoot || !sawConnect) {
    throw new Error("Go/WASM TUI boot logs were not observed in the browser console");
  }

  const statusText = await page.locator("#wasm-status").textContent();
  if (!statusText || !statusText.includes("Ready")) {
    throw new Error(`Unexpected runtime status: ${statusText ?? "<empty>"}`);
  }
} finally {
  await page.screenshot({
    path: path.join(outputDir, "smarttodo-demo-smoke.png"),
    fullPage: true,
  }).catch(() => {});
  await fs.writeFile(path.join(outputDir, "console.log"), logs.join("\n"), "utf8");
  await page.context().close();
  await browser.close();
}
