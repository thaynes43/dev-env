#!/usr/bin/env node
// Offline Attention presentation check. Opens local files in the session
// Chromium. It does not prove a physical device, deployed authentication, or Gate A.
const fs = require("fs");
const path = require("path");
const { chromium } = require("/usr/local/lib/node_modules/@playwright/mcp/node_modules/playwright");

const scenariosDir = process.argv[2];
const outDir = process.argv[3];
if (!scenariosDir || !outDir) {
  console.error("usage: browser_check.cjs <scenarios-dir> <output-dir>");
  process.exit(2);
}
const chrome = process.env.CHROME_PATH || "/opt/dev-env/ms-playwright/chromium-1247/chrome-linux64/chrome";
const viewports = [
  { name: "mobile-320", width: 320, height: 640 },
  { name: "mobile-390", width: 390, height: 844 },
  { name: "tablet-768", width: 768, height: 1024 },
  { name: "desktop-1280", width: 1280, height: 800 },
];
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
  const visible = (box) => box && box.w > 0 && box.h > 0 && box.left >= -1 && box.right <= observed.inner + 1 && box.scroll <= box.client + 1;
  for (const field of observed.fields) {
    for (const key of ["owner", "action", "actor", "source", "verified", "fresh", "title"]) {
      must(visible(field[key]) && field[key].text.length > 0, name + " " + viewport + " clipped " + key, failures);
    }
  }
  must(observed.fields.length === 0 || observed.described, name + " freshness not described", failures);
  for (const link of observed.links) {
    must(link.h >= 44 && link.w >= 44, name + " " + viewport + " link target " + link.w + "x" + link.h, failures);
    must(link.name.startsWith("Open canonical record") && link.name.includes("Open canonical record"), name + " inaccessible link name", failures);
    must(link.href.startsWith("https://work.haynesnetwork.com/work_packages/") && link.rel === "noreferrer", name + " unexpected link", failures);
    must(visible(link), name + " " + viewport + " link clipped", failures);
  }
  for (const summary of observed.summaries) {
    must(summary.h >= 44 && summary.w >= 44, name + " " + viewport + " summary target " + summary.w + "x" + summary.h, failures);
    must(summary.text.includes("Separate outcome, worker, review, authority and release"), name + " summary name", failures);
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
      const text = details ? details.innerText : "";
      return {
        open: !!(details && details.open),
        separate: text.includes("Task outcome") && text.includes("Worker liveness") && text.includes("Review") && text.includes("Authority") && text.includes("Release"),
        scroll: document.documentElement.scrollWidth,
        client: document.documentElement.clientWidth,
      };
    });
    must(opened.open && opened.separate, name + " " + viewport + " disclosure did not open", failures);
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

(async () => {
  const started = Date.now();
  fs.mkdirSync(outDir, { recursive: true });
  const failures = [];
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
      const page = await context.newPage();
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
  } finally {
    await browser.close();
  }
  const report = {
    startedEpochMs: started,
    elapsedMs: Date.now() - started,
    chrome,
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
