#!/usr/bin/env node
// Offline Attention presentation check. Opens local files in the session
// Chromium. It does not prove a physical device, deployed authentication, or Gate A.
const crypto = require("crypto");
const fs = require("fs");
const path = require("path");
const { spawn, spawnSync } = require("child_process");

const htmlNames = [
  "populated.html",
  "verified-empty.html",
  "unknown-listing.html",
  "stale-with-fresh-row.html",
  "fresh-row-unknown-listing.html",
  "future-listing.html",
  "hostile.html",
  "no-owner.html",
];
const textNames = ["denied.txt", "unavailable.txt"];
const refused = "Attention unavailable. Current task state is Unknown.\n";
const relevantEvidencePaths = ["web/portal", "scripts/trials/portal_attention"];

function sha256(bytes) {
  return crypto.createHash("sha256").update(bytes).digest("hex");
}

function gitEnv() {
  const env = { ...process.env };
  delete env.GIT_DIR;
  delete env.GIT_WORK_TREE;
  delete env.GIT_INDEX_FILE;
  return env;
}

function gitOutput(cwd, args) {
  const result = spawnSync("git", args, { cwd, encoding: "utf8", env: gitEnv() });
  if (result.error || result.status !== 0) {
    throw new Error("source head unavailable");
  }
  return result.stdout;
}

function checkoutEvidence(start) {
  const root = gitOutput(start, ["rev-parse", "--show-toplevel"]).trim();
  const head = gitOutput(root, ["rev-parse", "HEAD"]).trim();
  if (!/^[0-9a-f]{40}$/.test(head)) {
    throw new Error("source head unavailable");
  }
  const status = gitOutput(root, ["status", "--porcelain", "--", ...relevantEvidencePaths]);
  return { head, dirty: status.trim().length > 0 };
}

function assertExactSourceHead(manifestHead, evidence) {
  if (evidence.dirty) {
    throw new Error("source tree is dirty; refusing evidence as an exact source head");
  }
  if (manifestHead !== evidence.head) {
    throw new Error("scenario manifest source head does not match checkout HEAD; refusing a mismatched source head");
  }
}

function loadManifest(dir, sourceStart) {
  const manifestPath = path.join(dir, "manifest.json");
  if (!fs.existsSync(manifestPath)) {
    throw new Error("scenario manifest missing; refusing a partial or stale evidence set");
  }
  const raw = fs.readFileSync(manifestPath);
  let parsed;
  try { parsed = JSON.parse(raw.toString("utf8")); } catch (err) {
    throw new Error("scenario manifest is not JSON; refusing the evidence set");
  }
  if (!parsed.sourceHead || !/^[0-9a-f]{40}$/.test(parsed.sourceHead) || !Array.isArray(parsed.files) || parsed.files.length === 0) {
    throw new Error("scenario manifest has no source head or files; refusing the evidence set");
  }
  const names = new Set();
  for (const file of parsed.files) {
    if (!file.name || file.name.includes("/") || file.name.includes("..")) {
      throw new Error("scenario manifest entry is not a single file name");
    }
    const full = path.join(dir, file.name);
    if (!fs.existsSync(full)) throw new Error("scenario manifest entry missing: " + file.name);
    const got = sha256(fs.readFileSync(full));
    if (got !== file.sha256) throw new Error("scenario file hash mismatch for " + file.name + "; refusing a stale evidence set");
    names.add(file.name);
  }
  for (const required of htmlNames.concat(textNames)) {
    if (!names.has(required)) throw new Error("scenario manifest omits " + required);
  }
  for (const entry of fs.readdirSync(dir)) {
    if (entry !== "manifest.json" && !names.has(entry)) {
      throw new Error("scenario directory has " + entry + " outside the manifest; refusing a stale evidence set");
    }
  }
  const evidence = checkoutEvidence(sourceStart || __dirname);
  assertExactSourceHead(parsed.sourceHead, evidence);
  return { sourceHead: parsed.sourceHead, digest: sha256(raw), files: parsed.files.length };
}

if (require.main === module) {
  if (process.env.PORTAL_BROWSER_NICED !== "1") {
    const child = spawn("nice", ["-n", "19", process.execPath, ...process.argv.slice(1)], {
      stdio: "inherit",
      env: { ...process.env, PORTAL_BROWSER_NICED: "1" },
    });
    child.on("exit", (code) => process.exit(code === null ? 1 : code));
  } else {
    main();
  }
}

function main() {
const defaultModule = "/usr/local/lib/node_modules/@playwright/mcp/node_modules/playwright";
const modulePath = process.env.PORTAL_PLAYWRIGHT_MODULE || defaultModule;
const chrome = process.env.CHROME_PATH || "/opt/dev-env/ms-playwright/chromium-1247/chrome-linux64/chrome";
let playwright;
try {
  playwright = require(modulePath);
} catch (err) {
  console.error("Playwright module unavailable at " + modulePath + ".");
  console.error("That default is bound to the dev-env session image. Set PORTAL_PLAYWRIGHT_MODULE to an installed Playwright module.");
  console.error(err && err.message ? err.message : err);
  process.exit(1);
}
const chromium = playwright.chromium;
if (!chromium || typeof chromium.launch !== "function") {
  console.error("Playwright module at " + modulePath + " does not export chromium.launch.");
  process.exit(1);
}
if (!fs.existsSync(chrome)) {
  console.error("Chromium executable unavailable at " + chrome + ".");
  console.error("That default is bound to the dev-env session image. Set CHROME_PATH to the image Chromium binary.");
  process.exit(1);
}

const scenariosDir = process.argv[2];
const outDir = process.argv[3];
if (!scenariosDir || !outDir) {
  console.error("usage: browser_check.cjs <scenarios-dir> <output-dir>");
  process.exit(2);
}
const viewports = [
  { name: "mobile-320", width: 320, height: 640 },
  { name: "mobile-390", width: 390, height: 844 },
  { name: "tablet-768", width: 768, height: 1024 },
  { name: "desktop-1280", width: 1280, height: 800 },
];
function must(cond, message, failures) {
  if (!cond) failures.push(message);
}

async function inspectHTML(page, name, viewport, failures) {
  const file = path.resolve(scenariosDir, name);
  const requests = [];
  const onRequest = (request) => requests.push(request.url());
  page.on("request", onRequest);
  await page.goto("file://" + file, { waitUntil: "load" });
  const observed = await page.evaluate(() => {
    const box = (el) => {
      if (!el) return null;
      const rect = el.getBoundingClientRect();
      const style = getComputedStyle(el);
      return {
        w: rect.width,
        h: rect.height,
        left: rect.left,
        right: rect.right,
        scroll: el.scrollWidth,
        client: el.clientWidth,
        text: (el.textContent || "").trim(),
        outline: style.outlineStyle,
        outlineWidth: parseFloat(style.outlineWidth) || 0,
      };
    };
    const doc = document.documentElement;
    const links = [...document.querySelectorAll("a")].map((el) => ({
      ...box(el),
      name: el.getAttribute("aria-label") || "",
      href: el.getAttribute("href") || "",
      rel: el.getAttribute("rel") || "",
    }));
    const summaries = [...document.querySelectorAll("summary")].map(box);
    const fields = [...document.querySelectorAll("article")].map((article) => {
      const pick = (label) => {
        const nodes = [...article.querySelectorAll("dt")];
        const dt = nodes.find((node) => node.textContent === label);
        return dt ? box(dt.nextElementSibling) : null;
      };
      return {
        owner: pick("Owner"),
        action: pick("Next action"),
        actor: pick("Next actor"),
        source: pick("Canonical source"),
        verified: pick("Last verified"),
        fresh: box(article.querySelector("p.freshness")),
        title: box(article.querySelector("h3")),
      };
    });
    return {
      margin: getComputedStyle(document.body).marginTop,
      scroll: doc.scrollWidth,
      client: doc.clientWidth,
      inner: window.innerWidth,
      h1: document.querySelectorAll("h1").length,
      h2: [...document.querySelectorAll("h2")].map((el) => el.textContent.trim()),
      main: document.querySelectorAll("main").length,
      scripts: document.querySelectorAll("script, iframe, form, img, svg, object, embed").length,
      stylesheetLinks: document.querySelectorAll('link[rel="stylesheet"]').length,
      raw: document.documentElement.innerHTML,
      links,
      summaries,
      fields,
      described: [...document.querySelectorAll("article")].every((article) => {
        const id = article.getAttribute("aria-describedby");
        const fresh = article.querySelector("p.freshness");
        return id && document.getElementById(id) && fresh && document.getElementById(id).textContent.includes(fresh.textContent);
      }),
    };
  });
  page.off("request", onRequest);
  must(observed.margin === "0px", name + " " + viewport + " stylesheet did not apply", failures);
  must(observed.scroll <= observed.client + 1, name + " " + viewport + " horizontal overflow " + observed.scroll + ">" + observed.client, failures);
  must(observed.h1 === 1 && observed.main === 1, name + " " + viewport + " missing heading landmark", failures);
  must(observed.scripts === 0 && observed.stylesheetLinks === 0, name + " " + viewport + " active or remote content", failures);
  must(!observed.raw.includes("<script") && !observed.raw.includes("<img ") && !observed.raw.includes("<iframe") && !observed.raw.includes("<form"), name + " " + viewport + " raw active markup", failures);
  must(requests.every((url) => url.startsWith("file:")), name + " " + viewport + " remote request " + requests.join(" "), failures);
  const text = await page.locator("body").innerText();
  if (name === "unknown-listing.html") {
    must(text.includes("Attention unknown") && !text.includes("· 0"), name + " invented a zero count", failures);
    must(observed.h2.length === 0, name + " rendered group counts", failures);
  } else {
    for (const title of ["Needs you", "Needs worker", "Review returned", "Handoff missing"]) {
      must(observed.h2.some((heading) => heading.startsWith(title + " ·")), name + " " + viewport + " missing group " + title, failures);
    }
  }
  if (name === "verified-empty.html") {
    must(text.includes("Verified empty") && text.includes("No items in this group."), name + " verified empty copy", failures);
    must(observed.h2.every((heading) => heading.endsWith("· 0")), name + " verified empty count", failures);
  }
  if (name === "stale-with-fresh-row.html" || name === "fresh-row-unknown-listing.html" || name === "future-listing.html") {
    must(!text.includes("· 0"), name + " presented a current zero", failures);
    must(text.includes("Listing counts are not verified as current"), name + " hid the listing warning", failures);
  }
  if (name === "populated.html") {
    must(!text.includes("Listing counts are not verified as current"), name + " warned on a verified fixture", failures);
    must(text.includes("Stale evidence") && text.includes("Freshness unknown"), name + " lost row truth", failures);
  }
  if (name === "future-listing.html") must(text.includes("untrusted future timestamp"), name + " trusted a future timestamp", failures);
  if (name === "hostile.html") must(text.includes("Unknown") && !observed.raw.includes("<script>") && !observed.raw.includes("<img "), name + " executed hostile text", failures);
  if (name === "no-owner.html") must(text.includes("Unknown"), name + " hid a missing owner", failures);
  if (name === "populated.html") {
    const visibleFacts = await page.evaluate(() => {
      const sectionText = (title) => {
        const section = [...document.querySelectorAll("section")].find((node) => (node.querySelector("h2") || {}).textContent.startsWith(title));
        const primary = section && section.querySelector("dl.primary");
        return primary ? primary.innerText : "";
      };
      return { review: sectionText("Review returned"), handoff: sectionText("Handoff missing") };
    });
    must(visibleFacts.review.includes("Review") && visibleFacts.review.includes("Reviewed head"), name + " hid review evidence", failures);
    must(visibleFacts.handoff.includes("Task outcome") && visibleFacts.handoff.includes("Worker liveness"), name + " hid handoff evidence", failures);
  }
  const visible = (box) => box && box.w > 0 && box.h > 0 && box.left >= -1 && box.right <= observed.inner + 1 && box.scroll <= box.client + 1;
  for (const field of observed.fields) {
    for (const key of ["owner", "action", "actor", "source", "verified", "fresh", "title"]) {
      must(visible(field[key]) && field[key].text.length > 0, name + " " + viewport + " clipped " + key, failures);
    }
  }
  must(observed.fields.length === 0 || observed.described, name + " freshness not described", failures);
  for (const link of observed.links) {
    must(link.h >= 44 && link.w >= 44, name + " " + viewport + " link target " + link.w + "x" + link.h, failures);
    must(link.name.startsWith("Open canonical record"), name + " inaccessible link name", failures);
    must(link.href.startsWith("https://work.haynesnetwork.com/work_packages/") && link.rel === "noreferrer", name + " unexpected link", failures);
    must(visible(link), name + " " + viewport + " link clipped", failures);
  }
  for (const summary of observed.summaries) {
    must(summary.h >= 44 && summary.w >= 44, name + " " + viewport + " summary target " + summary.w + "x" + summary.h, failures);
    must(summary.text.startsWith("Separate ") && summary.text.length > 12, name + " summary name", failures);
    must(visible(summary), name + " " + viewport + " summary clipped", failures);
  }
  await page.screenshot({ path: path.join(outDir, name.replace(".html", "") + "-" + viewport + ".png"), fullPage: true });
  if (observed.links.length > 0) {
    await page.keyboard.press("Tab");
    const first = await page.evaluate(() => ({
      tag: document.activeElement.tagName,
      name: document.activeElement.getAttribute("aria-label") || "",
      outline: getComputedStyle(document.activeElement).outlineStyle,
      width: parseFloat(getComputedStyle(document.activeElement).outlineWidth) || 0,
    }));
    must(first.tag === "A" && first.name.startsWith("Open canonical record"), name + " " + viewport + " first tab " + first.tag, failures);
    must(first.outline !== "none" && first.width >= 3, name + " " + viewport + " link focus", failures);
    await page.keyboard.press("Tab");
    const second = await page.evaluate(() => ({
      tag: document.activeElement.tagName,
      text: (document.activeElement.textContent || "").trim(),
      outline: getComputedStyle(document.activeElement).outlineStyle,
      width: parseFloat(getComputedStyle(document.activeElement).outlineWidth) || 0,
      open: document.activeElement.parentElement ? document.activeElement.parentElement.open : false,
    }));
    must(second.tag === "SUMMARY", name + " " + viewport + " second tab " + second.tag, failures);
    must(second.outline !== "none" && second.width >= 3, name + " " + viewport + " summary focus", failures);
    await page.keyboard.press("Enter");
    const opened = await page.evaluate(() => {
      const details = document.activeElement.closest("details");
      const article = details ? details.closest("article") : null;
      const primary = article && article.querySelector("dl.primary") ? article.querySelector("dl.primary").innerText : "";
      const inside = details ? details.innerText : "";
      const labels = ["Task outcome", "Worker liveness", "Review", "Reviewed head", "Authority", "Release"];
      return {
        open: !!(details && details.open),
        covered: labels.every((label) => primary.includes(label) || inside.includes(label)),
        scroll: document.documentElement.scrollWidth,
        client: document.documentElement.clientWidth,
      };
    });
    must(opened.open && opened.covered, name + " " + viewport + " disclosure did not open", failures);
    must(opened.scroll <= opened.client + 1, name + " " + viewport + " overflow after disclosure", failures);
    if (name === "populated.html" && viewport === "mobile-390") {
      await page.screenshot({ path: path.join(outDir, "populated-mobile-390-open.png"), fullPage: true });
    }
  }
}

async function inspectText(page, name, viewport, failures) {
  const fileText = fs.readFileSync(path.join(scenariosDir, name), "utf8");
  must(fileText === refused, name + " is not the handler refusal", failures);
  await page.setContent("<!doctype html><html lang=en><head><meta charset=utf-8><title>Handler response</title></head><body><p>Handler text response. Not an HTML success page.</p><pre id=body></pre></body></html>");
  await page.locator("#body").evaluate((el, value) => { el.textContent = value; }, fileText);
  const shown = await page.locator("#body").innerText();
  must(shown.trim() === refused.trim() && !shown.includes("Verified empty"), name + " " + viewport + " text response", failures);
}

function packageVersion(start) {
  let dir = start;
  for (let i = 0; i < 5; i++) {
    const pkg = path.join(dir, "package.json");
    if (fs.existsSync(pkg)) {
      try { return JSON.parse(fs.readFileSync(pkg, "utf8")).version || "unknown"; } catch (err) { return "unknown"; }
    }
    const parent = path.dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  return "unknown";
}

(async () => {
  const started = Date.now();
  fs.mkdirSync(outDir, { recursive: true });
  const failures = [];
  let reportBrowserVersion = "unknown";
  let manifest;
  try {
    manifest = loadManifest(scenariosDir);
  } catch (err) {
    console.error(err.message || err);
    process.exit(1);
  }
  const browser = await chromium.launch({
    executablePath: chrome,
    headless: true,
    args: ["--no-sandbox", "--disable-dev-shm-usage"],
  });
  try {
    for (const viewport of viewports) {
      const context = await browser.newContext({
        viewport: { width: viewport.width, height: viewport.height },
        deviceScaleFactor: 1,
        colorScheme: "light",
        reducedMotion: "reduce",
      });
      const blocked = [];
      const remoteResponses = [];
      await context.route("**/*", async (route) => {
        const url = route.request().url();
        if (url.startsWith("file:") || url.startsWith("about:") || url.startsWith("data:")) {
          await route.continue();
          return;
        }
        blocked.push(url);
        await route.abort("blockedbyclient");
      });
      context.on("response", (response) => {
        const url = response.url();
        if (!url.startsWith("file:") && !url.startsWith("about:") && !url.startsWith("data:")) remoteResponses.push(url);
      });
      const page = await context.newPage();
      if (viewport.name === "mobile-320") {
        const probe = await context.newPage();
        const saw = new Promise((resolve) => {
          const timer = setTimeout(() => resolve(false), 1000);
          probe.on("request", (request) => {
            if (request.url().includes("example.invalid/portal-offline-probe")) {
              clearTimeout(timer);
              resolve(true);
            }
          });
        });
        await probe.setContent("<!doctype html><html><body><img src=\"https://example.invalid/portal-offline-probe\"></body></html>");
        must(await saw, "offline probe was not intercepted", failures);
        must(blocked.some((url) => url.includes("example.invalid/portal-offline-probe")), "offline probe was not aborted before network", failures);
        must(!remoteResponses.some((url) => url.includes("example.invalid")), "offline probe received a response", failures);
        await probe.close();
      }
      for (const name of htmlNames) {
        await inspectHTML(page, name, viewport.name, failures);
      }
      if (viewport.name === "mobile-320" || viewport.name === "desktop-1280") {
        for (const name of textNames) {
          await inspectText(page, name, viewport.name, failures);
          await page.screenshot({ path: path.join(outDir, name.replace(".txt", "") + "-" + viewport.name + ".png"), fullPage: true });
        }
      }
      await context.close();
    }
    reportBrowserVersion = await browser.version();
  } finally {
    await browser.close();
  }
  const report = {
    startedEpochMs: started,
    elapsedMs: Date.now() - started,
    chrome,
    browserVersion: reportBrowserVersion,
    playwrightModule: modulePath,
    playwrightVersion: packageVersion(modulePath),
    sourceHead: manifest.sourceHead,
    manifestDigest: manifest.digest,
    manifestFiles: manifest.files,
    scenariosDir,
    failures,
  };
  fs.writeFileSync(path.join(outDir, "report.json"), JSON.stringify(report, null, 2));
  console.log(JSON.stringify({ elapsedMs: report.elapsedMs, failures: failures.length }));
  if (failures.length) {
    console.error(failures.join("\n"));
    process.exit(1);
  }
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
}

module.exports = { loadManifest, checkoutEvidence, assertExactSourceHead, htmlNames, textNames };
