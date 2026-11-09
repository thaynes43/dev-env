package agentd

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func cursorTaskFixture(t *testing.T) (cursorScaffoldPlan, cursorTaskContext) {
	t.Helper()
	p := cursorScaffoldFixture(t)
	p.invocation = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	s := Settings{PodUID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", WorkspaceID: "synthetic-workspace",
		ManagedCodexTasks: true, ManagedChildDecisions: true, Home: filepath.Dir(p.stateRoot), StateDir: p.stateRoot}
	sess := protocol.Session{Name: "synthetic-task", Repo: "demo", Agent: protocol.AgentCodex,
		Mode: protocol.ModeTask, Model: "gpt-6.1-sol", SessionUID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		Workspace: &protocol.WorkspaceBinding{ID: s.WorkspaceID, SessionUID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc"}}
	l := Launch{Provider: sess.Agent, Session: sess.Name, SessionUID: sess.SessionUID, PodUID: s.PodUID,
		BootID: "synthetic-boot", ChildDecisions: true, NativeInvocationID: p.invocation,
		Dir: p.worktree, Prompt: p.prompt, Timeout: p.timeout, CreatedAt: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC),
		WorkspaceOwner: &taskOwner{Version: workspaceVersion, Workspace: s.WorkspaceID, Task: sess.Name,
			Repo: sess.Repo, Worktree: p.worktree, SessionUID: sess.SessionUID, PodUID: s.PodUID,
			Generation: 7, State: "owned"}}
	return p, cursorTaskContext{s, sess, l, l.BootID}
}

func copyCursorTaskContext(c cursorTaskContext) cursorTaskContext {
	if c.launch.WorkspaceOwner != nil {
		o := *c.launch.WorkspaceOwner
		c.launch.WorkspaceOwner = &o
	}
	if c.session.Workspace != nil {
		w := *c.session.Workspace
		c.session.Workspace = &w
	}
	return c
}

func TestCursorTaskMatchingBindingAndUnknownServedModel(t *testing.T) {
	p, current := cursorTaskFixture(t)
	task, err := newCursorBoundTask(p, current)
	if err != nil {
		t.Fatal(err)
	}
	decisions, _ := cursorScaffoldDecisionPipe(t)
	reads, starts := 0, 0
	result, err := runCursorBoundTask(context.Background(), task, func() (cursorTaskContext, error) {
		reads++
		return current, nil
	}, decisions, func(cursorScaffoldCommand) (cursorScaffoldPeer, error) {
		starts++
		return cursorScaffoldExited(cursorScaffoldStream(cursorScaffoldFrames(p)), nil), nil
	})
	if err != nil || reads != 2 || starts != 1 || !result.helper.promptComplete {
		t.Fatalf("matching synthetic binding did not complete: reads=%d starts=%d err=%v", reads, starts, err)
	}
	if result.binding != task.binding || result.binding.native.SessionUID != current.session.SessionUID ||
		result.binding.native.PodUID != current.settings.PodUID || result.binding.native.Generation != 7 ||
		result.binding.task != current.session.Name || result.helper.sessionID == result.binding.native.Session ||
		result.helper.requestedModel != p.model || result.helper.servedModel != "Unknown" {
		t.Fatal("helper result lost platform identity or inferred a served model")
	}
}

func TestCursorTaskMissingAndMismatchedIdentityRefused(t *testing.T) {
	changes := map[string]func(*cursorTaskContext){
		"missing task":                     func(c *cursorTaskContext) { c.launch.WorkspaceOwner.Task = "" },
		"wrong task":                       func(c *cursorTaskContext) { c.launch.WorkspaceOwner.Task = "another-task" },
		"missing owner":                    func(c *cursorTaskContext) { c.launch.WorkspaceOwner = nil },
		"missing Session":                  func(c *cursorTaskContext) { c.session.Name = "" },
		"missing Session UID":              func(c *cursorTaskContext) { c.session.SessionUID = "" },
		"wrong Session UID":                func(c *cursorTaskContext) { c.launch.SessionUID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd" },
		"missing Pod UID":                  func(c *cursorTaskContext) { c.settings.PodUID = "" },
		"wrong Pod UID":                    func(c *cursorTaskContext) { c.launch.PodUID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd" },
		"missing invocation":               func(c *cursorTaskContext) { c.launch.NativeInvocationID = "" },
		"missing boot":                     func(c *cursorTaskContext) { c.bootID = "" },
		"wrong boot":                       func(c *cursorTaskContext) { c.bootID = "replacement-boot" },
		"zero generation":                  func(c *cursorTaskContext) { c.launch.WorkspaceOwner.Generation = 0 },
		"stopped owner":                    func(c *cursorTaskContext) { c.launch.WorkspaceOwner.State = "stopped" },
		"already launched owner":           func(c *cursorTaskContext) { c.launch.WorkspaceOwner.Launched = true },
		"wrong worktree":                   func(c *cursorTaskContext) { c.launch.WorkspaceOwner.Worktree = "/synthetic-other" },
		"wrong repository":                 func(c *cursorTaskContext) { c.launch.WorkspaceOwner.Repo = "other" },
		"missing workspace":                func(c *cursorTaskContext) { c.session.Workspace = nil },
		"wrong workspace":                  func(c *cursorTaskContext) { c.session.Workspace.ID = "other" },
		"changed prompt":                   func(c *cursorTaskContext) { c.launch.Prompt = "different synthetic task" },
		"changed timeout":                  func(c *cursorTaskContext) { c.launch.Timeout += time.Second },
		"host kind is not Cursor identity": func(c *cursorTaskContext) { c.session.Agent = "cursor"; c.launch.Provider = "cursor" },
		"interactive":                      func(c *cursorTaskContext) { c.session.Mode = protocol.ModeLocal },
		"resume":                           func(c *cursorTaskContext) { c.launch.Resume = true },
		"adopted thread":                   func(c *cursorTaskContext) { c.launch.ConversationID = "prior-native-thread" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			p, c := cursorTaskFixture(t)
			change(&c)
			if _, err := newCursorBoundTask(p, c); !errors.Is(err, errCursorTaskBinding) {
				t.Fatal("missing or mismatched task identity was accepted")
			}
		})
	}
}

func TestCursorTaskConsistentReplacementStillCannotRebind(t *testing.T) {
	changes := map[string]func(*cursorTaskContext){
		"task generation": func(c *cursorTaskContext) { c.launch.WorkspaceOwner.Generation++ },
		"Session incarnation": func(c *cursorTaskContext) {
			uid := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
			c.session.SessionUID, c.session.Workspace.SessionUID = uid, uid
			c.launch.SessionUID, c.launch.WorkspaceOwner.SessionUID = uid, uid
		},
		"Pod incarnation": func(c *cursorTaskContext) {
			uid := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
			c.settings.PodUID, c.launch.PodUID, c.launch.WorkspaceOwner.PodUID = uid, uid, uid
		},
		"new boot": func(c *cursorTaskContext) { c.bootID = "new-boot"; c.launch.BootID = c.bootID },
	}
	for name, change := range changes {
		for _, afterCompletion := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/before-spawn", true: "/after-completion"}[afterCompletion], func(t *testing.T) {
				p, original := cursorTaskFixture(t)
				task, err := newCursorBoundTask(p, original)
				if err != nil {
					t.Fatal(err)
				}
				replacement := copyCursorTaskContext(original)
				change(&replacement)
				// It is internally coherent. Only exact first-offer comparison
				// prevents its receipt being attached to the replacement.
				if _, err := newCursorBoundTask(p, replacement); err != nil {
					t.Fatal(err)
				}
				decisions, _ := cursorScaffoldDecisionPipe(t)
				reads, starts := 0, 0
				result, err := runCursorBoundTask(context.Background(), task, func() (cursorTaskContext, error) {
					reads++
					if afterCompletion && reads == 1 {
						return original, nil
					}
					return replacement, nil
				}, decisions, func(cursorScaffoldCommand) (cursorScaffoldPeer, error) {
					starts++
					return cursorScaffoldExited(cursorScaffoldStream(cursorScaffoldFrames(p)), nil), nil
				})
				wantStarts := 0
				if afterCompletion {
					wantStarts = 1
				}
				if !errors.Is(err, errCursorTaskBinding) || starts != wantStarts || result.helper.promptComplete {
					t.Fatalf("replacement context relabeled a receipt: starts=%d err=%v", starts, err)
				}
			})
		}
	}
}

func TestCursorTaskContextFailureAndReplayNeverRespawn(t *testing.T) {
	p, current := cursorTaskFixture(t)
	task, err := newCursorBoundTask(p, current)
	if err != nil {
		t.Fatal(err)
	}
	decisions, _ := cursorScaffoldDecisionPipe(t)
	starts := 0
	start := func(cursorScaffoldCommand) (cursorScaffoldPeer, error) {
		starts++
		return cursorScaffoldExited(cursorScaffoldStream(cursorScaffoldFrames(p)), nil), nil
	}
	if _, err := runCursorBoundTask(context.Background(), task, nil, decisions, start); !errors.Is(err, errCursorTaskBinding) {
		t.Fatal("missing context accepted")
	}
	if _, err := runCursorBoundTask(context.Background(), task, func() (cursorTaskContext, error) { return current, errors.New("synthetic unavailable") }, decisions, start); !errors.Is(err, errCursorTaskBinding) {
		t.Fatal("uncertain context accepted")
	}
	if starts != 0 {
		t.Fatal("uncertain context spawned a peer")
	}
	read := func() (cursorTaskContext, error) { return current, nil }
	if _, err := runCursorBoundTask(context.Background(), task, read, decisions, start); err != nil {
		t.Fatal(err)
	}
	if _, err := runCursorBoundTask(context.Background(), task, read, decisions, start); !errors.Is(err, errCursorScaffoldReplay) {
		t.Fatal("initial prompt replay accepted")
	}
	if starts != 1 {
		t.Fatal("repeat invocation respawned a peer")
	}
}

func TestCursorTaskStateDirectoryIsPartOfCurrentIdentity(t *testing.T) {
	for _, missing := range []bool{false, true} {
		for _, after := range []bool{false, true} {
			name := map[bool]string{false: "changed", true: "missing"}[missing] + map[bool]string{false: "/before", true: "/after"}[after]
			t.Run(name, func(t *testing.T) {
				p, original := cursorTaskFixture(t)
				task, err := newCursorBoundTask(p, original)
				if err != nil {
					t.Fatal(err)
				}
				replacement := copyCursorTaskContext(original)
				replacement.settings.StateDir = p.stateRoot + "-replacement"
				if missing {
					replacement.settings.StateDir = ""
				}
				decisions, _ := cursorScaffoldDecisionPipe(t)
				reads, starts := 0, 0
				result, err := runCursorBoundTask(context.Background(), task, func() (cursorTaskContext, error) {
					reads++
					if after && reads == 1 {
						return original, nil
					}
					return replacement, nil
				}, decisions, func(cursorScaffoldCommand) (cursorScaffoldPeer, error) {
					starts++
					return cursorScaffoldExited(cursorScaffoldStream(cursorScaffoldFrames(p)), nil), nil
				})
				want := 0
				if after {
					want = 1
				}
				if !errors.Is(err, errCursorTaskBinding) || starts != want || result.helper.promptComplete {
					t.Fatalf("state directory was not fenced: starts=%d complete=%v err=%v", starts, result.helper.promptComplete, err)
				}
			})
		}
	}
}
