package apiserver

import (
	"context"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// A Queued session with a recorded reap leaves the API's queue count: the
// count and the leader share summonoutcome.Queued, and a recorded reap is a
// stop (OP779 N1, with OP778's shared predicate). The marker is written once
// by the reap route and nothing removes it, so the session cannot rejoin the
// count outside a ledger write.
func TestSummonQueueReapedSessionLeavesTheCount(t *testing.T) {
	for _, reaper := range []string{"human/dev-env-system/dev-env-human", "session/dev-agents/worker", ""} {
		t.Run("reaped by "+reaper, func(t *testing.T) {
			f := lanesFixture(t, queuePolicy(1, 1, 4, 1, 6))
			wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), "rem-responder-1")
			wantQueueRefusal(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), true, "concurrent")

			s := f.session("rem-responder-1")
			if !summonoutcome.Queued(s, false) {
				t.Fatal("rem-responder-1 is not Queued before its reap")
			}
			if s.Annotations == nil {
				s.Annotations = map[string]string{}
			}
			s.Annotations[v1alpha1.AnnotationReapedBy] = reaper
			if err := f.c.Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if got := f.session("rem-responder-1"); summonoutcome.Queued(got, false) || !got.DeletionTimestamp.IsZero() {
				t.Fatalf("rem-responder-1 with a recorded reap: Queued or deleting, annotations %v", got.Annotations)
			}

			// The place is free at once, with the reaped session still stored.
			f.now = f.now.Add(time.Minute)
			wantQueued(t, f, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), "rem-responder-2")
			// And the queue is full again: the limit still holds for the rest.
			wantQueueRefusal(t, summonPost(t, f, keyedRemediation("rem-responder-3", "alert-3")), true, "concurrent")
		})
	}
}
