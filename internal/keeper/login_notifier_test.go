package keeper

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// The notifier Run registers checks a Claude NeedsLogin from the hub against
// the named durable journal, behind the leader fence, before it pages. Only
// synthetic records and a local fake sender are used.
func TestLoginNotifierChecksTheClaudeJournalBeforePaging(t *testing.T) {
	for _, tc := range []struct {
		name    string
		stage   string // the journal the notifier should read
		journal string // ClaudeAuth.JournalSecret; empty means the default name
		holder  string // the Lease holder; the notifier is keeper-a
		want    int
	}{
		{name: "stale hub, journal ready", stage: claudeReady, holder: "keeper-a"},
		{name: "journal tombstone", stage: claudeNeedsLogin, holder: "keeper-a", want: 1},
		{name: "named journal ready", stage: claudeReady, journal: "synthetic-named-journal", holder: "keeper-a"},
		{name: "named journal tombstone", stage: claudeNeedsLogin, journal: "synthetic-named-journal", holder: "keeper-a", want: 1},
		{name: "another leader", stage: claudeNeedsLogin, holder: "keeper-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeFix(t)
			read := syntheticClaudeRecord(1)
			read.Stage = tc.stage
			other := syntheticClaudeRecord(1)
			other.Stage = claudeReady
			if tc.stage == claudeReady {
				other.Stage = claudeNeedsLogin
			}
			for _, r := range []*claudeRecord{&read, &other} {
				if r.Stage == claudeNeedsLogin {
					r.Refresh, r.RefreshExpiresAt = secretValue{}, time.Time{}
				}
			}
			if tc.journal == "" {
				f.seed(t, read)
			} else {
				// The default journal holds the opposite state, so only a read
				// of the named journal gives the wanted count.
				f.seed(t, other)
				named := &claudeJournal{Client: f.c, Secret: types.NamespacedName{Namespace: f.w.Journal.Secret.Namespace, Name: tc.journal}}
				if err := f.c.Create(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: named.Secret.Namespace, Name: named.Secret.Name, UID: "synthetic-named-incarnation"}}); err != nil {
					t.Fatal(err)
				}
				saveClaude(t, named, read)
			}
			f.holdLease(t, tc.holder)
			notices := newNoticeFix(t, claudeNow)
			o := Options{
				Namespace:  f.w.Journal.Secret.Namespace,
				Clock:      notices.clk,
				Log:        logr.Discard(),
				ClaudeAuth: ClaudeOptions{Enabled: true, JournalSecret: tc.journal},
				Notices: NoticeOptions{TokenFile: filepath.Join(notices.dir, "token"), UserFile: filepath.Join(notices.dir, "user"),
					URL: "https://example.invalid/unused"},
			}
			fence := &LeaseFence{Reader: f.c, Lease: f.lease, Identity: "keeper-a", Clock: f.clk, MaxAge: 10 * time.Second}
			nf, err := loginNotifier(o, notices.hub, f.c, fence)
			if err != nil {
				t.Fatal(err)
			}
			nf.Sender = &PushoverSender{URL: notices.fake.srv.URL, TokenFile: o.Notices.TokenFile, UserFile: o.Notices.UserFile}
			notices.hub.Report(ProviderClaude, LoginNeedsLogin, time.Time{})
			nf.Tick(context.Background())
			if got := notices.fake.count(); got != tc.want {
				t.Fatalf("synthetic notice count = %d, want %d", got, tc.want)
			}
		})
	}
}
