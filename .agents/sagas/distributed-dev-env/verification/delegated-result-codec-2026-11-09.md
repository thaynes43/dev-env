# Delegated result codec: verification record (2026-11-09)

This is the design and verification note for `internal/delegatedresult`, which dev-env #995 publishes. The code was authored and reviewed as a frozen Studio/Grok slice: patch SHA-256 `14bc7e33cd394f5042dec605e4f5328faf923d12db86fdc2ea7e4458e3974995`, base `90e98827`. Its normative input is the Studio delegated-result proposal contract (`CONTRACT.txt`, SHA-256 `1317cdc0dfd8c7c2b5f017666e1491d9053bbcf0abf8dab761171586738fbc37`). The text below is the slice author's note, reproduced unchanged from "Implemented surface" onward.

**What it changes at runtime:** nothing. No package imports it. Admission and Store have only a refusing `Disabled` implementation. There is no route, storage, credential, grant or provider call. Any caller, route, store or retention needs its own design first, following the docs-first rule. This record does not authorize one.

**Verification on 2026-11-09:**
- Coordinator dev-env-1109-080016 checked that the reviewed bytes are byte-identical to the patch.
- `go vet` is clean.
- `go test -race -count=1 ./internal/delegatedresult/` passes 24 of 24. These are the first executions of the authored tests.
- The publication commit adds only gofmt layout and errorlint test changes.
- `make lint` and `make check-layout` are clean.

## Implemented surface

- DecodeRequest bounds reader consumption to 409601 bytes and rejects input
  beyond 409600 raw bytes or 65536 decoded answer bytes. Exact UTF-8 bytes are
  retained, without normalization or truncation. Request schema is exactly
  schemaVersion integer literal 1 plus a string answer.
- UTF-8 and UTF-16 escape validation precedes encoding/json's replacement
  behavior. Duplicate keys, unknown keys, trailing JSON, forbidden C0/DEL,
  non-string answers and attribution overrides are refused with a fixed error.
- Transport is conservatively application/json with no parameters and no
  Content-Encoding. An integrating adapter must impose read deadlines and must
  pass actual headers, not manufactured defaults. Supporting charset parameters
  later requires deliberate tests, not permissive parsing by accident.
- Attempt is a frozen value with copied nullable served-model pointers.
  Equality checks exact answer bytes, key and all immutable provenance. The
  SHA-256 domain is delegated-result-v1 followed by NUL and answer UTF-8 bytes.
- Unknown served model stays nil/Unknown. Observed is a proposed representation
  requiring authenticated server evidence; neither constructors nor receipts
  establish that evidence. Requested models are not served-model evidence.
- DecodeReceipt verifies exact expected identity/provenance/digest/count,
  required fields, types and coherent unexpired timestamps. Optional prior
  receipt binds record/commit/time/expiry and requires Confirmed. First receipt
  cannot establish an unknowable commit ID or TTL: the trusted durable adapter
  and approved retention policy remain required. Validation is not a durability
  proof, current read authorization, user delivery acknowledgment or signature.
- Admission and Store are explicit unimplemented boundaries. Disabled is the
  only implementation supplied. No enabled constructor or coordinator is added.
  Values returns defensive copies for future adapters and confers no authority.

Key UID-shaped fields mirror the exact existing unexported
protocol.taskOutcomeIdentifier pattern. Project/task/catalog validation and
live identity checks remain the admission adapter's responsibility; bounded
labels are not an alternative to repository validators.

## Deliberately not implemented

No authoritative delegate admission/completion service, live auth adapter,
payload-reader grant, privacy screening, retention/quota approval, durable store,
status/read route, serialization lease, lifecycle controller, canary, production
registration or configuration. No provider, network, credentials or Kubernetes
use. No attempt to replace TaskOutcomeRecord or PublishedContext with full text.
Workspace rejection and original managed-capture restrictions remain required.

Strong cancellation ordering remains normative. A final Kubernetes read plus a
separate store write is insufficient. Choosing a lease or weaker observed-
revocation policy is a separate unresolved design and approval gate. Interfaces
and these codec tests cannot demonstrate global atomicity or storage durability.

The 18 Python proposal tests remain model-only evidence. This source ports the
codec/identity portions into Go, not the model's simulated live-state/store tests.
Auth/cancellation/ownership/quota tests require real approved adapters later.

## Verification status

See VERIFICATION.txt for actual observations and explicit NOT RUN checks.
This candidate is NOT gofmt-verified, compiled, unit-tested or vetted here.
No repository aggregate CI, envtest, provider or runtime acceptance was run.
