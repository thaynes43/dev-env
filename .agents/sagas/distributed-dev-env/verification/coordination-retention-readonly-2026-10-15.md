# Coordination retention read-only prerequisite — 2026-10-15

R3's source-only prerequisite adds `internal/coordretention`. It is not wired as
a runnable and performs no archive transport, mark, release, delete, flag, or
deployment action.

The package lists only explicitly enabled coordination kinds through its supplied
uncached reader, in the operator namespace, using pages of 10 for publications
and messages and 50 for claims and acknowledgements. It retains compact identity
summaries only and evaluates release eligibility fail-closed. The acknowledgement
archive result is a precomputed evidence seam; missing evidence stays Unknown and
names the required message for the later bulk lookup.

Focused fake tests exercise same-name Session UID replacement against the per-pass
memo and require explicit archive-holds proof for acknowledgement eligibility.
