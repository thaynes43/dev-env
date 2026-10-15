# Ollama offline candidate contract — 2026-10-15

Story #304 adds pure bounded data in the shared protocol package. D-33 still
selects OpenCode. The Codex OSS candidate is explicitly experimental. Neither
candidate is a production Session or an agentd Launch; a future adapter must
render its isolated configuration and obtain real admission.

Caller-supplied sanitized receipts bind backend, exact origin, full model ID,
SHA256 digest, tool manifest and provenance. Catalog advertisements never establish
model availability, inference, observed tools, cost or lifecycle control.
Candidate construction requires observed context capacity and every required
CLI/API/MCP transport entry from the exact manifest. Missing evidence returns
specific reasons and no candidate. Malformed receipts fail atomically.

Even a valid candidate retains explicit lease, household reservation, resource,
access/network and bounded execution/budget decisions. No Boolean grants authority.
The functions have no network, process, file, environment, credential or inference
access. Runtime authorization, availability and list/observe/steer/cancel remain
Unknown. Unknown cost uses the existing protocol CostObservation primitive.

Existing research is reused, with no repeat probe: both Ollama DNS reads failed;
a different llama-server catalog advertised only Muse Glimmer. That proves neither
Qwen availability nor Ollama unavailability. No Ollama/OpenCode CLI was found.
Synthetic Qwen receipts below are fixtures, never installed evidence.

Validation: all four focused `TestOllama` methods passed with finite subcases.
Independent native Sol review found a JSON case-fold duplicate override. Exact
canonical v1 field names now refuse ASCII and Unicode aliases; both regression
fixtures passed and the reviewer confirmed closure with no remaining findings.
Tests used cached Go 1.27.2, two CPUs, nice 19, package parallelism one, downloads
off and the shared test lock. Required exact-head CI remains a delivery gate.
The PR is held draft under the no-new-advisory-run direction; no ready conversion
or merge is authorized to this worker. All
runtime, authentication, security, egress, deployment, v1 and phase09/S11 gates
remain held. Smallest later decision: one exact endpoint/model/digest with observed
tools/context, lease/household/resource/network evidence and a bounded control
experiment. This source does not request or execute that decision.
