# Portal: last-reported allowance

OpenProject [#358](https://work.haynesnetwork.com/work_packages/358), Epic #187.
Presentation only. It adds no provider read, collector, credential or activation.

## What it shows

Sometimes the portal cannot trust a provider reading as current. A common case
is an official Codex read that reports 0% used and a reset time, but no
provider measurement time. Freshness is then not established, and the account
stays **Unknown**. Before this change the reading disappeared. Now the Usage
view keeps it, in a separate dashed block headed **Last reported, not current**.

Each line in the account section shows:

- the percent and its direction, exactly as the provider reported it, while
  the reading is 30 minutes old or newer and its reset is still ahead. The
  percent is then normal text, not grey. Older than 30 minutes, the same
  percent is greyed and the line says its age in plain words, for example
  "reported 3 hours ago". Once the reported reset time is at or before the
  page clock, the percent is not shown. The line says "This window is over:
  the reported reset has passed, so its percent is not shown." and "Last
  reading was <time>." The new window's use is unknown, not zero;
- the window, when the percent is still shown;
- **Received** time: when the collector received the reading (`ReceivedAt`),
  marked "not a provider time". When the provider did report its own
  measurement time, the line shows "provider measured" with that time
  instead. No provider time is invented;
- the source (Provider API, Provider display or Passive event);
- account confidence: **Account verified** only when the observation, the
  item binding and every source status for that provider are verified;
  otherwise **Account not verified**;
- the reported reset, as text only, and one fixed sentence saying why the
  value is not current, while that reset is still ahead. When a reported
  exact reset is at or before the page time, those two are replaced by the
  one sentence above and the last-reading time. It never says a reset
  happened: a passed reset time does not establish replenishment. A provider
  time later than the page time is flagged as in the future, on a line whose
  reset is still ahead.

The At a glance tile shows a compact form: at most two lines with value and
reason, then "+N more" linking to the account section. The account section
shows up to four lines and counts any beyond that.

The account badge stays Unknown while a reading is shown. If its reason would
read "No percent allowance reported" above a shown value, it reads "No current
percent allowance" instead. With no reading at all, a Claude or Codex tile
does not say Unknown. It says "Nothing has reported yet. A reading appears
when a new Codex task, or a remote Claude session, runs on a current image."
Cursor and Grok stay Not connected. A last-reported line draws no meter and
no countdown, and it never moves the page's "as of" time. The compact
Sessions strip shows only the state.

## When it is withheld

The mapper (`lastReportedAllowance` in
`web/portal/session/product_allowance.go`) shows nothing for a provider when
any of these hold:

- the read is partial, unsupported, conflicting, unconfigured or has
  duplicate measures (checked before this step);
- an item is unavailable, unvalidated or not a provider measurement;
- the receipt time does not parse;
- a current value conflicts with, duplicates or differs from the observation;
- either item binding, or any source status binding, is `mismatch`: such a
  reading may belong to another account;
- a source status validation is `rejected` or `conflict`;
- the items or source statuses name different accounts.

A measure whose current state is `not_applicable` (an alias of another
allowance) or `uncapped` is skipped. A later unavailable attempt recorded in
a source status does not hide the last accepted reading.

An unverified (`unknown`) binding is shown, labelled **Account not verified**.
At most four lines are shown per account. Measured, Stale and Not connected
accounts never carry one.

## Evidence

- Unit tests: `web/portal/session/product_allowance_last_reported_test.go`,
  `web/portal/allowance_last_reported_test.go`, and the preview state checks
  in `web/portal/preview/product_states_test.go`.
- Preview fixtures: `usage-allowance-last-reported.html` and
  `sessions-allowance-last-reported.html` from
  `go run ./web/portal/preview -product <dir> -pages <new dir>`.

Live data appears only once an allowance source is connected to the portal.
That connection is separate work (OP192, dev-env #850 and its rollout).
