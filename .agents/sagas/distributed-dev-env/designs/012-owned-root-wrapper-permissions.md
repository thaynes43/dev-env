# DESIGN-012: Owned wrapper permissions under a private umask

The first installed model-free roots attempt failed before verifier acceptance.
Its retained receipt proves command failure and exact image/version, not the
private internal cause. A separate bounded offline Git proof establishes that
umask 077 creates generated wrappers as 0600 while root ownership requires
0644. Fix this deterministic source boundary without asserting an unobserved
runtime cause. Roots operational campaign remains 1 of 3; the failed fixture
and all earlier evidence remain unchanged.

Enforce the declared permission on newly written owned AGENTS.md/CLAUDE.md
wrappers, including controlled refresh writes, independent of process umask.
The private HOME/project parent stays private; no other repository, provider
configuration or existing foreign/edited path is chmodded or adopted. Use
bounded existing ownership/preservation proof before mutation and re-confirm
the result. Retain dirty/index/ignored/local-commit/worktree/operation guards.

Paths are the private root write helper and focused real-Git permission tests.
Prove fresh creation and unchanged/controlled refresh under umask 077 produce
the owned declared mode; changed mode/foreign paths remain preserved/refused,
and no peer administrative state changes. Tests run sequentially under nice19,
GOMAXPROCS2/p2; no stress, wide loops, local image build or provider/cluster turn.
Required CI and native independent review govern source merge; Claude optional.

Publish the reviewed source/image, then separately review any verifier
diagnostic correction and a newly named bounded model-free check. Do not edit
or retry the failed Job. This is not native/provider-rule/MVP acceptance.
Keep #173/#91 open and preserve original fixtures, ledgers and sessions.
