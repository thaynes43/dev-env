"""Render finite synthetic metadata as inert HTML; no live portal integration."""

import argparse
import base64
import hashlib
import html
import json
import re
import unicodedata
from datetime import datetime
from pathlib import Path


MAX_BYTES = 64 * 1024
MAX_ROWS = 24
MAX_TEXT = 512
FRESH_SECONDS = 300  # Synthetic fixture parameter; no production SLA.
GROUPS = {
    "needs-you": "Needs you",
    "needs-worker": "Needs worker",
    "review-returned": "Review returned",
    "handoff-missing": "Handoff missing",
}
ROW_KEYS = {
    "id", "title", "group", "canonicalWorkPackage", "owner", "nextActor",
    "nextAction", "sourceUpdatedAt", "verifiedAt", "workerState",
    "taskOutcome", "reviewHead",
}
STYLE = """
* { box-sizing: border-box; }
body { margin: 0; background: #f3f5f8; color: #17243b;
  font: 1rem/1.5 system-ui, sans-serif; }
main { max-width: 72rem; margin: auto; padding: clamp(1rem, 4vw, 2.5rem); }
h1 { font-size: clamp(2rem, 7vw, 3rem); margin: .4rem 0; line-height: 1.15; }
h2 { font-size: 1.2rem; margin: 1.5rem 0 .7rem; }
h3 { font-size: 1.12rem; margin: 0 0 .5rem; }
p { margin: .5rem 0; }
.eyebrow { color: #334c75; font-weight: 650; }
.notice { padding: 1rem; border: 1px solid #b5c3d9; border-radius: .7rem; background: #fff; }
.groups { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 29rem), 1fr)); gap: 0 1.3rem; }
section, article, dd { min-width: 0; }
article { background: #fff; padding: 1.15rem; border-radius: .85rem;
  border: 1px solid #c8d1df; margin-bottom: .8rem; overflow-wrap: anywhere; }
.freshness { display: inline-block; border-radius: .35rem; padding: .15rem .45rem;
  background: #eef2f7; color: #34445b; font-weight: 600; font-size: .9rem; }
.stale, .unknown { background: #fff2d5; color: #694600; }
dl { display: grid; grid-template-columns: minmax(5rem, 7.5rem) minmax(0, 1fr); gap: .5rem .75rem; margin: 1rem 0; }
dt { color: #465774; } dd { margin: 0; }
a { color: #174fb1; display: inline-flex; align-items: center; min-height: 44px; }
a:focus-visible { outline: 3px solid #174fb1; outline-offset: 3px; }
time, code { overflow-wrap: anywhere; }
footer { color: #465774; margin-top: 1.5rem; }
""".strip()


def timestamp(value):
    if value is None:
        return None
    if not isinstance(value, str) or not re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z", value):
        raise ValueError("expected UTC timestamp or null")
    return datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ")


def text(value):
    if value is None:
        return "Unknown"
    if not isinstance(value, str) or len(value) > MAX_TEXT:
        raise ValueError("expected bounded text or null")
    # Drop terminal/control characters, including bidi controls. Never interpret ANSI.
    value = re.sub(r"\x1b\[[0-?]*[ -/]*[@-~]", "", value)
    value = "".join(char for char in value if unicodedata.category(char) not in {"Cc", "Cf"})
    return html.escape(value, quote=True) if value.strip() else "Unknown"


def validate(document):
    if not isinstance(document, dict) or set(document) != {"version", "asOf", "readState", "items"}:
        raise ValueError("expected exact fixture envelope")
    if type(document["version"]) is not int or document["version"] != 1:
        raise ValueError("unknown fixture version")
    if timestamp(document["asOf"]) is None:
        raise ValueError("missing fixture observation time")
    if document["readState"] not in ("verified", "unavailable", "denied"):
        raise ValueError("unknown read state")
    rows = document["items"]
    if not isinstance(rows, list) or len(rows) > MAX_ROWS:
        raise ValueError("too many fixture rows")
    if document["readState"] != "verified" and rows:
        raise ValueError("failed read cannot supply verified rows")
    seen = set()
    for row in rows:
        if not isinstance(row, dict) or set(row) != ROW_KEYS:
            raise ValueError("expected exact metadata row; extra payload is forbidden")
        if not isinstance(row["id"], str) or not re.fullmatch(r"[a-z][a-z0-9-]{0,63}", row["id"]) or row["id"] in seen:
            raise ValueError("invalid or duplicate fixture row id")
        seen.add(row["id"])
        if not isinstance(row["group"], str) or row["group"] not in GROUPS:
            raise ValueError("unknown Attention group")
        work = row["canonicalWorkPackage"]
        if type(work) is not int or not 1 <= work <= 2**31 - 1:
            raise ValueError("expected numeric canonical work package")
        for field in ("title", "owner", "nextActor", "nextAction", "workerState", "taskOutcome"):
            text(row[field])
        for field in ("sourceUpdatedAt", "verifiedAt"):
            timestamp(row[field])
        head = row["reviewHead"]
        if head is not None and (not isinstance(head, str) or not re.fullmatch(r"[0-9a-f]{40}", head)):
            raise ValueError("expected exact reviewed commit or null")


def age(value, as_of):
    moment = timestamp(value)
    if moment is None:
        return "Unknown"
    seconds = int((as_of - moment).total_seconds())
    if seconds < 0:
        return "Unknown (future timestamp)"
    if seconds < 60:
        return f"{seconds}s"
    if seconds < 3600:
        return f"{seconds // 60}m"
    return f"{seconds // 3600}h"


def freshness(value, as_of):
    moment = timestamp(value)
    if moment is None or moment > as_of:
        return "unknown", "Freshness unknown"
    if (as_of - moment).total_seconds() > FRESH_SECONDS:
        return "stale", "Stale evidence"
    return "fresh", "Verified within fixture window"


def card(row, as_of):
    state, label = freshness(row["verifiedAt"], as_of)
    updated, verified = timestamp(row["sourceUpdatedAt"]), timestamp(row["verifiedAt"])
    if updated is not None and verified is not None and verified < updated:
        state, label = "unknown", "Verification predates source update"
    fields = [
        ("Owner", row["owner"]), ("Next actor", row["nextActor"]),
        ("Next action", row["nextAction"]), ("Task outcome", row["taskOutcome"]),
        ("Worker state", row["workerState"]), ("Reviewed head", row["reviewHead"]),
        ("Source age", age(row["sourceUpdatedAt"], as_of)),
        ("Source updated", row["sourceUpdatedAt"]),
        ("Last verified", row["verifiedAt"]),
    ]
    details = "".join(f"<dt>{title}</dt><dd>{text(value)}</dd>" for title, value in fields)
    work = row["canonicalWorkPackage"]  # Validated integer; never an input URL.
    return (f'<article aria-labelledby="row-{row["id"]}"><h3 id="row-{row["id"]}">{text(row["title"])}</h3>'
            f'<span class="freshness {state}">{label}</span><dl>{details}</dl>'
            f'<a href="https://work.haynesnetwork.com/work_packages/{work}" rel="noreferrer" '
            f'aria-label="Canonical fixture record for {text(row["title"])} ({row["id"]}), work package {work}">'
            f'Canonical fixture record · #{work}</a></article>')


def render(document):
    validate(document)
    as_of = timestamp(document["asOf"])
    if document["readState"] != "verified":
        content = '<p class="notice">Attention unavailable: the fixture read was ' + document["readState"] + '. Current task state is Unknown.</p>'
    elif not document["items"]:
        content = '<p class="notice">Verified empty fixture: no Attention items in this synthetic snapshot.</p>'
    else:
        sections = []
        for key, title in GROUPS.items():
            rows = [row for row in document["items"] if row["group"] == key]
            sections.append(f'<section aria-labelledby="group-{key}"><h2 id="group-{key}">{title} · {len(rows)}</h2>'
                            + "".join(card(row, as_of) for row in rows) + '</section>')
        content = '<div class="groups">' + "".join(sections) + '</div>'
    digest = base64.b64encode(hashlib.sha256(STYLE.encode()).digest()).decode()
    policy = "default-src 'none'; style-src 'sha256-" + digest + "'; base-uri 'none'; form-action 'none'"
    return ('<!doctype html><html lang="en"><head><meta charset="utf-8">'
            '<meta name="viewport" content="width=device-width, initial-scale=1">'
            f'<meta http-equiv="Content-Security-Policy" content="{html.escape(policy, quote=True)}">'
            f'<title>Attention · Synthetic preview</title><style>{STYLE}</style></head><body><main>'
            '<header><p class="eyebrow">Dev-env · Read-only fixture</p><h1>Attention</h1>'
            '<p>Who owns the next step, and when was it last verified?</p>'
            '<p class="notice"><strong>Synthetic preview. No live data or actions.</strong> '
            'Groups come from fixture metadata. Worker state and task outcome stay separate.</p>'
            f'<p>Fixture observation: <time datetime="{document["asOf"]}">{document["asOf"]}</time></p></header>{content}'
            '<footer>Source preparation only. Review, CI, release, installation and acceptance need separate evidence.</footer>'
            '</main></body></html>\n')


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON field")
        result[key] = value
    return result


def load(path):
    with path.open("rb") as source:
        raw = source.read(MAX_BYTES + 1)
    if len(raw) > MAX_BYTES:
        raise ValueError("fixture exceeds byte bound")
    try:
        return json.loads(raw.decode("utf-8"), object_pairs_hook=unique_object)
    except (ValueError, RecursionError):
        raise ValueError("invalid synthetic JSON fixture") from None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        page = render(load(Path(__file__).with_name("fixtures.json")))
    except (ValueError, OSError, RecursionError):
        parser.exit(1, "Invalid or unavailable synthetic fixture; no output written.\n")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(page, encoding="utf-8")
    print("Wrote synthetic read-only preview:", args.output)


if __name__ == "__main__":
    main()
