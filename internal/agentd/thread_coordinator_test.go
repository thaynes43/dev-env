package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func coordinatorEntryFixture() (Settings, protocol.Session, CodexThreadRequest) {
	s := Settings{WorkbenchPod: true, CodexCoordinatorHost: "reserved", CodexCoordinatorUID: "session-uid", PodUID: "pod-uid"}
	sess := protocol.Session{Name: "reserved", SessionUID: "session-uid", Agent: "codex", Mode: "remote"}
	req := goodThreadRequest()
	req.Coordinator = true
	req.Host = "reserved"
	req.Model = "gpt-6.1-sol"
	req.Effort = "medium"
	req.ExpectedSessionUID = "session-uid"
	req.ExpectedPodUID = "pod-uid"
	return s, sess, req
}
func TestCodexCoordinatorEntryMetadataAndUIDs(t *testing.T) {
	for _, guard := range []string{"ordinary-intent", "missing-metadata", "metadata-host", "metadata-uid", "session-name", "session-uid", "pod-account", "target-host", "target-session-uid", "target-pod-uid", "missing-session-fence", "missing-pod-fence", "model", "effort", "coordinator-on-pool"} {
		t.Run(guard, func(t *testing.T) {
			s, sess, req := coordinatorEntryFixture()
			if !codexCoordinatorEntry(s, sess, req) {
				t.Fatal("valid metadata refused")
			}
			switch guard {
			case "ordinary-intent":
				req.Coordinator = false
			case "missing-metadata":
				s.CodexCoordinatorHost = ""
				s.CodexCoordinatorUID = ""
			case "metadata-host":
				s.CodexCoordinatorHost = "other"
			case "metadata-uid":
				s.CodexCoordinatorUID = "other"
			case "session-name":
				sess.Name = "other"
			case "session-uid":
				sess.SessionUID = "other"
			case "pod-account":
				s.WorkbenchPod = false
			case "target-host":
				req.Host = "other"
			case "target-session-uid":
				req.ExpectedSessionUID = "other"
			case "target-pod-uid":
				req.ExpectedPodUID = "other"
			case "missing-session-fence":
				req.ExpectedSessionUID = ""
			case "missing-pod-fence":
				req.ExpectedPodUID = ""
			case "model":
				req.Model = "gpt-6-astra"
			case "effort":
				req.Effort = "high"
			case "coordinator-on-pool":
				s.WorkbenchPod = false
				s.CodexCoordinatorHost = ""
				s.CodexCoordinatorUID = ""
			}
			if codexCoordinatorEntry(s, sess, req) {
				t.Fatal("removed deciding guard admitted", guard)
			}
		})
	}
	s := Settings{}
	sess := protocol.Session{}
	req := goodThreadRequest()
	if !codexCoordinatorEntry(s, sess, req) {
		t.Fatal("ordinary launch changed")
	}
	req.ExpectedSessionUID = "stale"
	if codexCoordinatorEntry(s, sess, req) {
		t.Fatal("partial ordinary UID fence ignored")
	}
}

func TestCodexCoordinatorTitleAfterExactBinding(t *testing.T) {
	for _, end := range []string{"confirmed", "lost-ack", "ambiguous"} {
		t.Run(end, func(t *testing.T) {
			s, sess, cwd := threadHost(t)
			meta, bound, req := coordinatorEntryFixture()
			s.WorkbenchPod, s.CodexCoordinatorHost, s.CodexCoordinatorUID, s.PodUID = meta.WorkbenchPod, meta.CodexCoordinatorHost, meta.CodexCoordinatorUID, meta.PodUID
			sess.Name, sess.SessionUID = bound.Name, bound.SessionUID
			f := newThreadFake(cwd)
			d := f.deps()
			titles := 0
			if end == "ambiguous" {
				other := f.all[0]
				other.ID = twoThread
				f.all = append(f.all, other)
			}
			d.title = func(_ context.Context, got Settings, pid int, id, dir string) error {
				titles++
				if id != newThread || dir != cwd || pid != 4242 || len(f.launches) != 1 || got.PodUID != s.PodUID {
					t.Fatal("title before exact binding")
				}
				if end == "lost-ack" {
					return errors.New("ACK lost")
				}
				return nil
			}
			result := startCodexThread(context.Background(), s, sess, req, d)
			if len(f.launches) != 1 {
				t.Fatalf("relaunches: %d", len(f.launches))
			}
			switch end {
			case "confirmed":
				if result.Outcome != apiv1.ThreadResponded || titles != 1 {
					t.Fatalf("%+v titles=%d", result, titles)
				}
			case "lost-ack":
				if result.Outcome != apiv1.ThreadUnconfirmed || result.ThreadID != newThread || result.Error.Code != "title_unconfirmed" || titles != 1 {
					t.Fatalf("%+v titles=%d", result, titles)
				}
			case "ambiguous":
				if result.Outcome != apiv1.ThreadUnconfirmed || titles != 0 {
					t.Fatalf("%+v titles=%d", result, titles)
				}
			}
		})
	}
}

func TestCodexCoordinatorNativeTitleRPC(t *testing.T) {
	for _, fault := range []string{"none", "read-id", "read-cwd", "set-ack", "readback-name", "readback-id"} {
		t.Run(fault, func(t *testing.T) {
			reads, sets := 0, 0
			s, seen := threadRPCFixture(t, func(method string, params map[string]any) any {
				switch method {
				case "thread/read":
					reads++
					if params["threadId"] != newThread || params["includeTurns"] != false {
						t.Error("not exact metadata read", params)
					}
					id, cwd, name := newThread, "/synthetic/repo", ""
					if fault == "read-id" || fault == "readback-id" && reads > 1 {
						id = twoThread
					}
					if fault == "read-cwd" {
						cwd = "/other"
					}
					if reads > 1 && fault != "readback-name" {
						name = codexCoordinatorTitle
					}
					return map[string]any{"thread": map[string]any{"id": id, "cwd": cwd, "name": name}}
				case "thread/name/set":
					sets++
					if params["threadId"] != newThread || params["name"] != codexCoordinatorTitle || len(params) != 2 {
						t.Error("wrong title mutation", params)
					}
					if fault == "set-ack" {
						return nil
					}
					return map[string]any{}
				}
				return nil
			})
			s.CodexCoordinatorHost, s.CodexCoordinatorUID, s.PodUID = "reserved", "session-uid", "pod-uid"
			err := setCodexCoordinatorTitle(context.Background(), s, os.Getpid(), newThread, "/synthetic/repo")
			if (fault == "none") != (err == nil) {
				t.Fatalf("fault=%s err=%v", fault, err)
			}
			if sets > 1 || (fault == "read-id" || fault == "read-cwd") && sets != 0 {
				t.Fatalf("sets=%d", sets)
			}
			for _, call := range *seen {
				switch call["method"] {
				case "initialize", "initialized", "thread/read", "thread/name/set":
				default:
					t.Fatal("unexpected native operation", call)
				}
			}
		})
	}
}

func TestCodexCoordinatorOneUseHostRefuses(t *testing.T) {
	s, sess, d, deps, mutations := rcControlFixture(t)
	s.CodexCoordinatorHost = sess.Name
	s.CodexCoordinatorUID = sess.SessionUID
	s.WorkbenchPod = true
	for _, op := range []string{"preflight", "start"} {
		res, err := codexRCControl(context.Background(), s, sess, op, d, deps)
		if err != nil || res.State != "refused" || *mutations != 0 {
			t.Fatalf("one-use coordinator mutation: %+v %v count=%d", res, err, *mutations)
		}
	}
}

func TestCodexCoordinatorTitlePinnedSchema(t *testing.T) {
	for name, digest := range map[string]string{
		"ThreadSetNameParams":   "8e334c816bbaeb80f807aca61bf659246f3f60c245c21b5cb7e3ca4ee3ce4820",
		"ThreadSetNameResponse": "f8763716433feea94f7c1ebd59b6cea4c261432e83240c13a8d49ba8185aa567",
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", "codex_thread_launch", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != digest {
			t.Fatal("pinned setter schema changed", name)
		}
		var doc map[string]any
		if json.Unmarshal(raw, &doc) != nil || doc["type"] != "object" {
			t.Fatal("setter shape")
		}
		if name == "ThreadSetNameParams" {
			props := doc["properties"].(map[string]any)
			if len(props) != 2 || props["name"] == nil || props["threadId"] == nil {
				t.Fatal("setter params")
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "codex_thread_launch", "ThreadListResponse.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		t.Fatal("thread schema")
	}
	thread := doc["definitions"].(map[string]any)["Thread"].(map[string]any)
	if thread["properties"].(map[string]any)["name"] == nil {
		t.Fatal("native metadata name missing")
	}
}

func TestCodexCoordinatorTitleInvalidBindingDoesNotConnect(t *testing.T) {
	for _, fault := range []string{"id", "host-metadata", "session-metadata", "pod-uid"} {
		t.Run(fault, func(t *testing.T) {
			s, seen := threadRPCFixture(t, func(string, map[string]any) any { return nil })
			s.CodexCoordinatorHost, s.CodexCoordinatorUID, s.PodUID = "reserved", "session-uid", "pod-uid"
			id := newThread
			switch fault {
			case "id":
				id = "not-an-id"
			case "host-metadata":
				s.CodexCoordinatorHost = ""
			case "session-metadata":
				s.CodexCoordinatorUID = ""
			case "pod-uid":
				s.PodUID = ""
			}
			if setCodexCoordinatorTitle(context.Background(), s, os.Getpid(), id, "/synthetic/repo") == nil || len(*seen) != 0 {
				t.Fatal("invalid binding reached native RPC")
			}
		})
	}
}
