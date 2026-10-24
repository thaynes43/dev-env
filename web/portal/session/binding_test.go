package session

import (
	"strings"
	"testing"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

func TestFixtureClaimKeysAreCanonicalAndNotWorkPackageIDs(t *testing.T) {
	for _, task := range approvedTasks {
		binding := testBindings()[task]
		if binding.taskKey == task || strings.HasPrefix(binding.taskKey, "issue:"+task) {
			t.Fatalf("work package %s was normalized into its claim key %s", task, binding.taskKey)
		}
		if !binding.valid() || !taskclaim.ValidIdentity(coord.TaskIdentity{Project: binding.project, Repository: binding.repository, TaskKey: binding.taskKey}) {
			t.Fatalf("fixture binding for %s is not a valid TaskClaim identity", task)
		}
		if canonicalTaskKey.MatchString(binding.taskKey) != taskclaim.ValidIdentity(coord.TaskIdentity{Project: "dev-env", Repository: "dev-env", TaskKey: binding.taskKey}) {
			t.Fatalf("local grammar drifted for %s", binding.taskKey)
		}
	}
	for _, key := range []string{"178", "120", "issue:0", "issue:01", "issue:178", "task:attention-120", "issue:501", "issue:900122", "task:wp.123"} {
		local := canonicalTaskKey.MatchString(key)
		shared := taskclaim.ValidIdentity(coord.TaskIdentity{Project: "dev-env", Repository: "dev-env", TaskKey: key})
		if local != shared {
			t.Fatalf("grammar drift on %s", key)
		}
	}
}

func TestSessionBindingsRequireExplicitCanonicalKeys(t *testing.T) {
	valid := bindingDocumentJSON(t, testBindings())
	if _, err := parseBindings(valid); err != nil {
		t.Fatal("explicit canonical bindings rejected")
	}
	bare := strings.ReplaceAll(string(valid), `"taskKey":"issue:501"`, `"taskKey":"178"`)
	derived := strings.ReplaceAll(string(valid), `"taskKey":"issue:501"`, `"taskKey":"issue:178"`)
	if _, err := parseBindings([]byte(bare)); err == nil {
		t.Fatal("bare work-package number accepted as a claim key")
	}
	parsed, err := parseBindings([]byte(derived))
	if err != nil || parsed["178"].taskKey != "issue:178" {
		t.Fatal("explicit issue key was rewritten or rejected")
	}
	if parsed["178"].taskKey == "178" {
		t.Fatal("explicit key collapsed to the work-package id")
	}
	for _, broken := range []string{
		strings.ReplaceAll(string(valid), `"schema":2`, `"schema":1`),
		strings.ReplaceAll(string(valid), `"taskKey":"issue:501"`, `"taskKey":""`),
		strings.Replace(string(valid), `"task":"120"`, `"task":"124"`, 1),
		strings.Replace(string(valid), `"taskKey":"task:attention-121"`, `"taskKey":"task:attention-120"`, 1),
		strings.Replace(string(valid), `"claimUid":"claim-uid-121"`, `"claimUid":"claim-uid-120"`, 1),
		strings.Replace(string(valid), `"project":"dev-env"`, `"project":"other"`, 1),
		strings.Replace(string(valid), `"repository":"dev-env"`, `"repository":"other"`, 1),
		strings.Replace(string(valid), `"sessionUid":"synthetic-session-uid"`, `"sessionUid":"other-session-uid"`, 1),
		strings.Replace(string(valid), `"taskKey":"issue:501",`, ``, 1),
	} {
		if _, err := parseBindings([]byte(broken)); err == nil {
			t.Fatalf("incomplete or ambiguous binding accepted: %s", broken)
		}
	}
}

func bindingDocumentJSON(t *testing.T, bindings map[string]sessionBinding) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"schema":2,"sessions":[`)
	for i, task := range approvedTasks {
		item := bindings[task]
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"task":"` + task + `","project":"` + item.project + `","repository":"` + item.repository + `","taskKey":"` + item.taskKey + `","session":"` + item.session + `","sessionUid":"` + item.sessionUID + `","claim":"` + item.claim + `","claimUid":"` + item.claimUID + `"}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}
