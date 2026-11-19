# 0.169.1 interpreted rollout fields

Minimal synthetic JSONL projection of the fields the usage reader interprets,
plus new ignored metadata and token_usage_record. Not a captured native run,
login, provider reply, full schema fixture or model entitlement proof.
The source audit pins protocol/src/protocol.rs and history/rollout_payload.rs at
c376cf7396eff83ce4c6ac61b9553df142e7a0be. Expected cumulative usage is 10 input,
5 output (3 cached input); duplicate snapshots must not be added. The test runs
both 6.2 slugs through the model-shape reader, independent of launch admission.
