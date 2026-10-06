# .agents/sagas: multi-plan initiatives

A **saga** is an initiative too big for one PR: a vision, the decisions, and a backlog
of plans that agents and humans carry out over many sessions. The conventions follow
haynes-ops' `.agents/sagas/README.md`, plus numbered ADRs and designs.

## Layout of a saga

```
.agents/sagas/<saga>/
├── README.md          vision, architecture at a glance, hard news, decision log, backlog index
├── KICKOFF.md         work order for the next build session (optional)
├── adrs/NNN-<slug>.md      decisions (MADR); consequences get C-NN ids
├── designs/NNN-<slug>.md   how: components, APIs, flows; D-NN decisions, Q-NN questions
├── research/R-NN-<slug>.md findings that feed a design; not decisions
└── backlog/NN-<slug>.md    plans, numbered in rough order; each declares its dependencies
```

The repo's front door, above every saga, is [`.agents/HANDOFF.md`](../HANDOFF.md).

## Conventions

- **Read the saga README first.** It is the entry point for any session working the
  saga.
- **Status** for ADRs and designs: `Draft → Proposed → Accepted → (Superseded by NNN |
  Deprecated)`. An Accepted ADR is not edited; a new ADR supersedes it.
- **Ids are stable.** `D-NN` (a decision settled in a design), `Q-NN` (a question only
  the owner can answer), `C-NN` (an ADR consequence), `S-NN` (a spike). Never
  renumber or reuse one; refer to them across docs as `DESIGN-001 Q-03`.
- **Q-NN entries** list two to four options with the recommended one first, each with
  a one-line consequence. The question goes to Tom one at a time with
  AskUserQuestion; the Q-NN entry is the record, not the ask. His answer is folded
  back in as a dated ruling, and the README's decision log row changes to DECIDED.
- **Decision log** rows carry a date and a status: OPEN, PROPOSED, DECIDED, REVISED.
- **Plans** mark `Status: done` with the completing PR when they finish. They are not
  deleted: the backlog doubles as history.

## Active sagas

- [distributed-dev-env](distributed-dev-env/README.md): dev-env v2, one pod per agent
  session, run by an operator.
