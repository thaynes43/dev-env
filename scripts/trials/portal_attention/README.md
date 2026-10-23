# Synthetic Attention preview

Source-only fixture for [Story 178](https://work.haynesnetwork.com/work_packages/178).
It displays the existing Astra inbox groups without implementing workflow
transitions, a portal service, authentication, commands or a production stack.
Every example is synthetic. The canonical link points to the fixture's work
record, not a claim about a live task.

From the repository root, using the already installed Python standard library:

```sh
nice -n 19 python3 -B -m unittest discover -s scripts/trials/portal_attention -p 'test_*.py' -v
nice -n 19 python3 -B scripts/trials/portal_attention/preview.py --output bin/portal-attention.html
```

Open the resulting HTML as a file. It contains no JavaScript, network fetch,
remote asset, shell, iframe, form or credential. Links are constructed from
numeric work-package IDs on the fixed canonical tracker origin. No server is
needed. This Python renderer is a disposable fixture tool, not a portal/BFF
technology choice.

`fixtures.json` fixes its synthetic observation time. Each row explicitly
supplies its group, owner, next actor/action, source update and verification
timestamps, task outcome, worker state and reviewed head. The renderer never
derives one workflow field from another. Missing evidence stays Unknown.
The five-minute freshness boundary is a **fixture parameter**, not a selected
production SLA. A denied/unavailable read cannot render a verified empty state.

The finite tests cover presentation boundaries in threat-map T05–T07, T09
and T12: escaping and inert text, bounded/strict metadata, constructed links,
independent worker/task state, stale/missing/future evidence, and unavailable
versus verified-empty results. They do not test OIDC, backend authorization,
session/CSRF handling, audit storage, commands, rate limiting or deployed
Authentik. Those remain pending in the
[source/threat map](../../../.agents/sagas/distributed-dev-env/designs/029-portal-foundations.md).

Run only this finite suite, one process under `nice -n 19`, with at most two
CPUs available to heavier checks. No real lifecycle, load, media, model or
credential fixtures. Parent coordinates required independent review once the
PR head is stable; this tool grants no runtime authority.

## Go presentation check

The Go handler in `web/portal` is the presentation under test. The command
below writes the same synthetic states through that handler and opens them in
local Chromium. A screenshot from this check is not device or deployed-auth
acceptance. Run it from the repository root, one checker run at a time, under `nice -n 19`.

Playwright and Chromium default to the paths in the dev-env session image. Set `PORTAL_PLAYWRIGHT_MODULE` or `CHROME_PATH` to override them. Standard output is the elapsed time and failure count. `report.json` records the Playwright module version, Chromium version, source head and scenario manifest digest. The checker aborts any non-file request before the network. It refuses a stale or partial scenario directory, a manifest source head that is not this checkout's HEAD, and uncommitted changes under `web/portal` or `scripts/trials/portal_attention`. The scenario command writes only a new directory. An existing destination or `<dir>.previous` stays unchanged.

```sh
GOTOOLCHAIN=local GOMAXPROCS=2 nice -n 19 \
  go run -p 1 ./web/portal/preview --scenarios /tmp/portal-attention-scenarios
nice -n 19 node scripts/trials/portal_attention/browser_check.cjs \
  /tmp/portal-attention-scenarios /tmp/portal-attention-browser
```
