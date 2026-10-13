package agentd

import (
	"path"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The classification of DESIGN-025 6.3: every top-level entry of each provider
// directory, and of agentd's state directory, gets one class by its name. An
// entry in no class refuses the export (unknown_entry), so a new layout blocks
// expiry until a source change classifies it (ADR-005 C-34). Nothing is ever
// dropped silently: what is not exported is listed in the plan.
//
// The names were confirmed on 2026-10-13 against the agent image's pinned
// versions in images/agent/Dockerfile (Claude Code 2.1.296, Codex 0.160.1):
// a names-only listing of a HOME those versions wrote, Claude Code's own list
// of config-directory names and the Codex source at rust-v0.160.1. DESIGN-025
// 6.3 records what the confirmation added.

// historyKind is what a history entry must be. A history name that is some
// other kind of file refuses the export (unexpected_entry); a link is listed
// with class link and never followed, whatever its name.
type historyKind int

const (
	kindAny historyKind = iota
	kindDir
	kindFile
)

// historyRule classifies the names that match pattern (path.Match, one
// segment).
type historyRule struct {
	pattern string
	// class is "" for history, which is exported; otherwise one of the
	// protocol's exclusion classes.
	class string
	// kind applies to history only.
	kind historyKind
}

const classHistory = ""

// claudeLayout is $CLAUDE_CONFIG_DIR.
var claudeLayout = []historyRule{
	// History: the transcripts and per-session subdirectories (subagents,
	// tool-results, workflows), the prompt history, jobs and feedback.
	{"projects", classHistory, kindDir},
	{"history.jsonl", classHistory, kindFile},
	{"jobs", classHistory, kindDir},
	{"feedback", classHistory, kindDir},
	// Copies of user or workspace files (ADR-006).
	{"file-history", protocol.HistoryClassUserCopy, kindAny},
	{"paste-cache", protocol.HistoryClassUserCopy, kindAny},
	{"uploads", protocol.HistoryClassUserCopy, kindAny},
	// Configuration, caches and runtime state.
	{"CLAUDE.md", protocol.HistoryClassNotHistory, kindAny},
	{"agents", protocol.HistoryClassNotHistory, kindAny},
	{"settings.json", protocol.HistoryClassNotHistory, kindAny},
	{"settings.json.*", protocol.HistoryClassNotHistory, kindAny},
	{"plugins", protocol.HistoryClassNotHistory, kindAny},
	{"skills", protocol.HistoryClassNotHistory, kindAny},
	{"cache", protocol.HistoryClassNotHistory, kindAny},
	{"state", protocol.HistoryClassNotHistory, kindAny},
	{"daemon", protocol.HistoryClassNotHistory, kindAny},
	{"daemon.lock", protocol.HistoryClassNotHistory, kindAny},
	{"daemon.status.json", protocol.HistoryClassNotHistory, kindAny},
	{"policy-limits.json*", protocol.HistoryClassNotHistory, kindAny},
	{"remote-settings.json", protocol.HistoryClassNotHistory, kindAny},
	{"statusline-command.sh", protocol.HistoryClassNotHistory, kindAny},
	{".last-cleanup", protocol.HistoryClassNotHistory, kindAny},
	// The event queue of Claude Code's own telemetry (H4's confirmation).
	{"telemetry", protocol.HistoryClassNotHistory, kindAny},
	// The runtime session registry, with key files.
	{"sessions", protocol.HistoryClassNotHistory, kindAny},
	// Account and organization identifiers.
	{".claude.json", protocol.HistoryClassNotHistory, kindAny},
	{"backups", protocol.HistoryClassNotHistory, kindAny},
	// Environment captures, which can hold secrets.
	{"shell-snapshots", protocol.HistoryClassNotHistory, kindAny},
	{"session-env", protocol.HistoryClassNotHistory, kindAny},
	// Credentials: never exported, listed by name only.
	{".credentials.json", protocol.HistoryClassCredential, kindAny},
	{".credentials.json.*", protocol.HistoryClassCredential, kindAny},
}

// codexLayout is $CODEX_HOME.
var codexLayout = []historyRule{
	// History: rollouts, the session index, prompt history, the state
	// databases with their -wal and -shm files, and what the model made.
	{"sessions", classHistory, kindDir},
	{"archived_sessions", classHistory, kindDir},
	{"session_index.jsonl", classHistory, kindFile},
	{"history.jsonl", classHistory, kindFile},
	{"state_*.sqlite*", classHistory, kindFile},
	{"memories_*.sqlite*", classHistory, kindFile},
	{"goals_*.sqlite*", classHistory, kindFile},
	{"queue_*.sqlite*", classHistory, kindFile},
	// The thread item store (codex-rs/thread-store, H4's confirmation).
	{"thread_history_*.sqlite*", classHistory, kindFile},
	{"generated_images", classHistory, kindDir},
	// Inline visualizations the model drew, by date (H4's confirmation).
	{"visualizations", classHistory, kindDir},
	// Copies of user files (ADR-006).
	{"attachments", protocol.HistoryClassUserCopy, kindAny},
	// Configuration, caches and runtime state.
	{"config.toml", protocol.HistoryClassNotHistory, kindAny},
	{"AGENTS.md", protocol.HistoryClassNotHistory, kindAny},
	{"rules", protocol.HistoryClassNotHistory, kindAny},
	{"skills", protocol.HistoryClassNotHistory, kindAny},
	{"plugins", protocol.HistoryClassNotHistory, kindAny},
	{"packages", protocol.HistoryClassNotHistory, kindAny},
	{"cache", protocol.HistoryClassNotHistory, kindAny},
	{"models_cache.json", protocol.HistoryClassNotHistory, kindAny},
	{"app-server-control", protocol.HistoryClassNotHistory, kindAny},
	{"app-server-daemon", protocol.HistoryClassNotHistory, kindAny},
	{"log", protocol.HistoryClassNotHistory, kindAny},
	{"logs_*.sqlite*", protocol.HistoryClassNotHistory, kindAny},
	{"installation_id", protocol.HistoryClassNotHistory, kindAny},
	{".tmp", protocol.HistoryClassNotHistory, kindAny},
	// Scratch files and the update check (H4's confirmation).
	{"tmp", protocol.HistoryClassNotHistory, kindAny},
	{"version.json", protocol.HistoryClassNotHistory, kindAny},
	{"rollout-migrations", protocol.HistoryClassNotHistory, kindAny},
	{"thread-writer-locks", protocol.HistoryClassNotHistory, kindAny},
	{"mcp-oauth-locks", protocol.HistoryClassNotHistory, kindAny},
	{".sqlite-maintenance.lock", protocol.HistoryClassNotHistory, kindAny},
	{".personality_migration", protocol.HistoryClassNotHistory, kindAny},
	{".sandbox_migration", protocol.HistoryClassNotHistory, kindAny},
	// Environment captures.
	{"shell_snapshots", protocol.HistoryClassNotHistory, kindAny},
	// Credentials.
	{"auth.json", protocol.HistoryClassCredential, kindAny},
	{"auth.json.*", protocol.HistoryClassCredential, kindAny},
	{".credentials.json", protocol.HistoryClassCredential, kindAny},
	{".credentials.json.*", protocol.HistoryClassCredential, kindAny},
}

// agentdLayout is ~/.agentd. TestHistoryLayoutCoversAgentdState scans
// internal/agentd for every name agentd writes there and fails when this table
// lacks one, so a new agentd file is classified in the change that adds it.
var agentdLayout = []historyRule{
	// History: the task records, the child decisions, captured assistant text
	// and the initial usage observation.
	{"boot.json", classHistory, kindFile},
	{"launch.json", classHistory, kindFile},
	{"resume.json", classHistory, kindFile},
	{"task-result.json", classHistory, kindFile},
	{"tui-exit.json", classHistory, kindFile},
	{"task-events.jsonl", classHistory, kindFile},
	{"task-source.json", classHistory, kindFile},
	{"child-decision.json", classHistory, kindFile},
	{"decisions", classHistory, kindDir},
	{"managed-output", classHistory, kindDir},
	{"initial-usage", classHistory, kindDir},
	// Locks.
	{"agent.pid", protocol.HistoryClassNotHistory, kindAny},
	{"deliver.lock", protocol.HistoryClassNotHistory, kindAny},
	{"rescue.lock", protocol.HistoryClassNotHistory, kindAny},
	{"project-bootstrap.lock", protocol.HistoryClassNotHistory, kindAny},
	{"codex-host-supervisor.lock", protocol.HistoryClassNotHistory, kindAny},
	{"native-invocation.lock", protocol.HistoryClassNotHistory, kindAny},
	{"workspace-supervisor.lock", protocol.HistoryClassNotHistory, kindAny},
	// Runtime and setup state.
	{"native-invocation.json", protocol.HistoryClassNotHistory, kindAny},
	{"native-started.json", protocol.HistoryClassNotHistory, kindAny},
	{"codex-access-generation.json", protocol.HistoryClassNotHistory, kindAny},
	{"codex-host-config.json", protocol.HistoryClassNotHistory, kindAny},
	{"codex-host-start-intent.json", protocol.HistoryClassNotHistory, kindAny},
	{"codex-host-preparation.json", protocol.HistoryClassNotHistory, kindAny},
	{"common-rule-ownership.json", protocol.HistoryClassNotHistory, kindAny},
	{"common-launch-inputs.json", protocol.HistoryClassNotHistory, kindAny},
	{"mcp-managed.json", protocol.HistoryClassNotHistory, kindAny},
	{"project-bootstrap.json", protocol.HistoryClassNotHistory, kindAny},
	{"project-snapshot.json", protocol.HistoryClassNotHistory, kindAny},
	{"project-rules.md", protocol.HistoryClassNotHistory, kindAny},
	{"project-roots", protocol.HistoryClassNotHistory, kindAny},
	{"project-sync", protocol.HistoryClassNotHistory, kindAny},
	{"workspace-admission.json", protocol.HistoryClassNotHistory, kindAny},
	{"workspace-stop-request.json", protocol.HistoryClassNotHistory, kindAny},
	// Publication intents: a published context's key and hash, kept so a
	// retry never publishes twice. The context itself is DESIGN-024's.
	{"published-context.*.json", protocol.HistoryClassNotHistory, kindAny},
	// Git files the rescue covers.
	{"rescue-*.bundle", protocol.HistoryClassNotHistory, kindAny},
	{"rescue-index-*", protocol.HistoryClassNotHistory, kindAny},
	// Temporary copies (files.go).
	{".*.agentd-*", protocol.HistoryClassNotHistory, kindAny},
}

// classifyHistoryName finds the rule for a top-level name. The first matching
// rule wins; ok is false for a name in no class.
func classifyHistoryName(rules []historyRule, name string) (historyRule, bool) {
	for _, r := range rules {
		if ok, err := path.Match(r.pattern, name); err == nil && ok {
			return r, true
		}
	}
	return historyRule{}, false
}
