package coordinatorwait

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestCrashAroundResultPersistence(t *testing.T) {
	for _, phase := range []string{"before-save", "after-rename", "after-save-before-print", "after-print"} {
		t.Run(phase, func(t *testing.T) {
			s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
			must(t, err)
			defer s.Close()
			r := record()
			r.Note("first", "43", false)
			r.Note("second", "44", false)
			must(t, s.Save(r))
			rt, _, _ := fakeRuntime(t)
			rt.Save = s.Save
			n := 0
			if phase == "before-save" || phase == "after-rename" {
				s.sync = func(*os.File) error {
					n++
					if n == 1 && phase == "before-save" || n == 2 && phase == "after-rename" {
						return errors.New("crash")
					}
					return nil
				}
			}
			v, err := rt.finish(r, epoch, false)
			if phase == "before-save" || phase == "after-rename" {
				if err == nil || v != nil {
					t.Fatal("printed before durable save")
				}
			} else {
				must(t, err)
			}
			original := *r.Outstanding
			printed := []byte(nil)
			if phase == "after-print" {
				printed, err = json.Marshal(v)
				must(t, err)
			}
			// Discard the process's state. Restart only from the committed record.
			recovered, err := s.Load()
			must(t, err)
			must(t, recovered.Bind(recovered.Options, "", false, false))
			replay := recovered.Take(epoch.Add(time.Minute), false)
			if replay == nil || replay.ID != original.ID || replay.Note != "first" {
				t.Fatal("event lost or given two IDs")
			}
			if phase != "before-save" && !reflect.DeepEqual(*replay, original) {
				t.Fatal("stored result changed on replay")
			}
			if printed != nil {
				again, err := json.Marshal(replay)
				must(t, err)
				if string(printed) != string(again) {
					t.Fatal("printed result changed after crash")
				}
			}
			if recovered.Bind(recovered.Options, "wrong", false, false) == nil {
				t.Fatal("wrong ACK skipped stored result")
			}
			must(t, recovered.Bind(recovered.Options, replay.ID, false, false))
			second := requireKind(t, recovered, epoch.Add(time.Minute), false, "note")
			if second.Note != "second" || second.ID == replay.ID {
				t.Fatal("next event lost or reused ID")
			}
			ack(t, recovered, false)
			if recovered.Take(epoch.Add(time.Minute), false) != nil {
				t.Fatal("acknowledged event replayed")
			}
		})
	}
}
func TestUnreadableStoredResultRequiresReconciliation(t *testing.T) {
	s, err := OpenStore(t.TempDir(), "session-uid", "tool-call")
	must(t, err)
	defer s.Close()
	r := record()
	r.Note("child-question", "43", false)
	v := requireKind(t, r, epoch, false, "note")
	must(t, s.Save(r))
	if r.Bind(r.Options, v.ID, false, true) == nil {
		t.Fatal("reconciled accepted for readable result")
	}
	data, err := json.Marshal(r)
	must(t, err)
	var raw map[string]json.RawMessage
	must(t, json.Unmarshal(data, &raw))
	raw["outstanding"] = json.RawMessage(`{"kind":false}`)
	data, err = json.Marshal(raw)
	must(t, err)
	f, err := s.root.OpenFile(s.name+".json", os.O_WRONLY|os.O_TRUNC, 0600)
	must(t, err)
	_, err = f.Write(data)
	must(t, err)
	must(t, f.Close())
	loaded, err := s.Load()
	must(t, err)
	if loaded.Outstanding != nil || loaded.LastID != v.ID {
		t.Fatal("unreadable result lost recovery identity")
	}
	if loaded.Bind(loaded.Options, v.ID, false, false) == nil {
		t.Fatal("unreadable result skipped without reconciliation")
	}
	must(t, loaded.Bind(loaded.Options, v.ID, false, true))
	requireKind(t, loaded, epoch, false, "resync")
}
func TestRuntimeReplayDoesNotReadOrReplaceResult(t *testing.T) {
	r := record()
	r.Note("child-question", "43", false)
	v := requireKind(t, r, epoch, false, "note")
	rt, _, _ := fakeRuntime(t)
	rt.Scan = func() (Snapshot, error) { t.Fatal("replay scanned"); return Snapshot{}, nil }
	rt.Notes = func(context.Context, string, time.Duration) (Note, error) {
		t.Error("replay read parent")
		return Note{}, nil
	}
	rt.Stop = func() (bool, error) { t.Fatal("replay replaced result with stop"); return true, nil }
	out, err := rt.Run(context.Background(), r)
	must(t, err)
	if !reflect.DeepEqual(out, v) {
		t.Fatal("replay changed stored result")
	}
}
