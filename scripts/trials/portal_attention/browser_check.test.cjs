const test = require("node:test");
const assert = require("node:assert/strict");
const crypto = require("crypto");
const fs = require("fs");
const os = require("os");
const path = require("path");
const { spawnSync } = require("child_process");
const { loadManifest, htmlNames, textNames } = require("./browser_check.cjs");

function git(root, args) {
  const result = spawnSync("git", ["-C", root, "-c", "user.email=portal-test@example.com", "-c", "user.name=portal-test", "-c", "commit.gpgsign=false", "-c", "core.autocrlf=false", ...args], {
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(args.join(" ") + "\n" + result.stdout + result.stderr);
  }
  return result.stdout;
}

function fixture() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "portal-head-"));
  git(root, ["init", "-b", "main"]);
  fs.mkdirSync(path.join(root, "web/portal"), { recursive: true });
  fs.mkdirSync(path.join(root, "scripts/trials/portal_attention"), { recursive: true });
  fs.writeFileSync(path.join(root, "web/portal/attention.html"), "clean");
  fs.writeFileSync(path.join(root, "scripts/trials/portal_attention/browser_check.cjs"), "clean");
  git(root, ["add", "web/portal", "scripts/trials/portal_attention"]);
  git(root, ["commit", "-m", "fixture"]);
  return { root, head: git(root, ["rev-parse", "HEAD"]).trim() };
}

function scenarioDir(head) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "portal-scenarios-"));
  const files = htmlNames.concat(textNames).map((name) => {
    const body = Buffer.from(name + "-body");
    fs.writeFileSync(path.join(dir, name), body);
    return { name, sha256: crypto.createHash("sha256").update(body).digest("hex") };
  });
  fs.writeFileSync(path.join(dir, "manifest.json"), JSON.stringify({ sourceHead: head, files }, null, 2) + "\n");
  return dir;
}

function remove(dir) {
  fs.rmSync(dir, { recursive: true, force: true });
}

test("checker accepts the exact checkout head", () => {
  const { root, head } = fixture();
  const dir = scenarioDir(head);
  try {
    const got = loadManifest(dir, root);
    assert.equal(got.sourceHead, head);
    assert.equal(got.files, htmlNames.length + textNames.length);
  } finally {
    remove(root);
    remove(dir);
  }
});

test("checker refuses a swapped source head", () => {
  const { root, head } = fixture();
  const swapped = (head[0] === "0" ? "1" : "0") + head.slice(1);
  const dir = scenarioDir(swapped);
  try {
    assert.throws(() => loadManifest(dir, root), /does not match checkout HEAD/);
    assert.equal(fs.readFileSync(path.join(dir, "populated.html"), "utf8"), "populated.html-body");
  } finally {
    remove(root);
    remove(dir);
  }
});

test("checker refuses a dirty relevant tree as an exact head", () => {
  const { root, head } = fixture();
  fs.writeFileSync(path.join(root, "web/portal/attention.html"), "dirty");
  const dir = scenarioDir(head);
  try {
    assert.throws(() => loadManifest(dir, root), /refusing evidence as an exact source head/);
  } finally {
    remove(root);
    remove(dir);
  }
});

test("checker accepts a head when only an unrelated file is dirty", () => {
  const { root, head } = fixture();
  fs.writeFileSync(path.join(root, "notes.md"), "other");
  const dir = scenarioDir(head);
  try {
    assert.equal(loadManifest(dir, root).sourceHead, head);
  } finally {
    remove(root);
    remove(dir);
  }
});

test("checker still refuses a tampered scenario file", () => {
  const { root, head } = fixture();
  const dir = scenarioDir(head);
  try {
    fs.writeFileSync(path.join(dir, "populated.html"), "tampered");
    assert.throws(() => loadManifest(dir, root), /hash mismatch for populated.html/);
  } finally {
    remove(root);
    remove(dir);
  }
});
