# Portal: last-reported allowance

OpenProject [#358](https://work.haynesnetwork.com/work_packages/358), Epic #187.
Presentation only. It adds no provider read, collector, credential or activation.

## What it shows

Sometimes the portal cannot trust a provider reading as current. A common case
is an official Codex read that reports 0% used and a reset time, but no
provider measurement time. Freshness is then not established, and the account
stays **Unknown**. Before this change the reading disappeared. Now the Usage
view keeps it, in a separate dashed block headed **Last reported, not current**.

Each line shows:

- the percent and its direction, exactly as the provider reported it;
- the window;
- **Read** time: when the collector received the reading (`ReceivedAt`). This
  is not a provider time, and none is invented;
- the source (Provider API, Provider display or Passive event);
- account confidence: **Account verified** or **Account not verified**;
- the reported reset, as text only, and one fixed sentence saying why the
  value is not current.

The account badge stays Unknown, and its reason line stays as before. A
last-reported line draws no meter and no countdown, and it never moves the
page's "as of" time. The compact Sessions strip shows only the state.

## When it is withheld

The mapper (`lastReportedAllowance` in
`web/portal/session/product_allowance.go`) shows nothing for a provider when
any of these hold:

- the read is partial, unsupported, conflicting, unconfigured or has
  duplicate measures (checked before this step);
- an item is unavailable, unvalidated or not a provider measurement;
- the receipt time does not parse;
- a current value conflicts with, duplicates or differs from the observation;
- either binding is `mismatch`: such a reading may belong to another account;
- the items or source statuses name different accounts.

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
