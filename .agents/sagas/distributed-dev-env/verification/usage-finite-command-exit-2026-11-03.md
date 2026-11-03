# Finite usage command exit — source verification

A finite metadata command may close stdout while its normal shutdown is still
running. The reader previously killed its owned process group immediately after
EOF, then required a successful process exit. That can turn valid metadata into
an acquisition failure. The zero-turn Claude builtin had the same lifecycle.

A bounded FIFO fixture reproduced the failure: it emits finite output, closes
stdout and waits for an explicit test release before exiting successfully. The
original metadata reader failed in 0.004 seconds because it killed the command
before that release. No vendor client, credential or provider call is involved.

The corrected finite readers observe natural exit with Linux `waitid` and
`WNOWAIT`. They then use the existing process-group termination and direct-child
reap. The unreaped leader retains the owned group identity until cleanup, while
the existing command deadline and group cancellation bound the wait. Interactive
Codex/Cursor capture termination is unchanged. Nonzero exits remain failures;
the Claude stream must still prove a successful zero-turn, zero-token result.

The full `internal/usagecollector` race suite passed in 2.236 seconds with
`GOMAXPROCS=2`, `-p 1` and `nice -n 19`. Added cases cover controlled EOF before
natural exit for metadata and the Claude builtin, a deadline after stdout closes,
and valid output followed by a nonzero exit. Existing parser, account binding,
receipt and owned-child cleanup tests also passed.

This establishes a source bug and its regression coverage. It does not establish
the cause of any particular live provider refusal or prove recurring ingestion.
Live acceptance still requires signed publication, reviewed installation, real
three-provider cycles, normalized API/native reads, UI refresh/stale/error evidence
and recovery with the original journal and clocks. No account, reader, host,
permission, credential, cadence, backoff or expiry changes are part of this fix.
