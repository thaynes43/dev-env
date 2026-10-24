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
	explicitName := claimObjectName("dev-env", "dev-env", "issue:178")
	derived := strings.ReplaceAll(string(valid), `"taskKey":"issue:501"`, `"taskKey":"issue:178"`)
	derived = strings.ReplaceAll(derived, `"claim":"`+testBindings()["178"].claim+`"`, `"claim":"`+explicitName+`"`)
	if _, err := parseBindings([]byte(bare)); err == nil {
		t.Fatal("bare work-package number accepted as a claim key")
	}
	parsed, err := parseBindings([]byte(derived))
	if err != nil || parsed["178"].taskKey != "issue:178" || parsed["178"].claim != explicitName {
		t.Fatal("explicit issue key was rewritten or rejected")
	}
	if parsed["178"].taskKey == "178" || parsed["178"].claim == claimObjectName("dev-env", "dev-env", "178") {
		t.Fatal("explicit key collapsed to the work-package id")
	}
	real := testBindings()["178"].claim
	for _, broken := range []string{
		strings.ReplaceAll(string(valid), `"claim":"`+real+`"`, `"claim":"claim-178"`),
		strings.ReplaceAll(string(valid), `"claim":"`+real+`"`, `"claim":"`+strings.ToUpper(real)+`"`),
		strings.ReplaceAll(string(valid), `"claim":"`+real+`"`, `"claim":"`+real[:len(real)-1]+`"`),
		strings.ReplaceAll(string(valid), `"claim":"`+real+`"`, `"claim":"`+real+"/extra"+`"`),
		strings.ReplaceAll(string(valid), `"claim":"`+real+`"`, `"claim":"`+real+"?project=dev-env"+`"`),
		strings.ReplaceAll(string(valid), `"claim":"`+real+`"`, `"claim":"`+real+"\\n"+`"`),
		strings.ReplaceAll(string(valid), `"claim":"`+real+`"`, `"claim":"task-claim.`+strings.Repeat("g", 32)+`.`+strings.Repeat("a", 32)+`"`),
		strings.ReplaceAll(string(valid), `"claim":"`+real+`"`, `"claim":"`+testBindings()["120"].claim+`"`),
		strings.ReplaceAll(string(valid), `"session":"synthetic-session"`, `"session":"`+real+`"`),
	} {
		if _, err := parseBindings([]byte(broken)); err == nil {
			t.Fatalf("malformed claim or widened session name accepted: %s", broken)
		}
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

func TestClaimNamesMatchTaskClaimContract(t *testing.T) {
	for _, task := range approvedTasks {
		binding := testBindings()[task]
		id := coord.TaskIdentity{Project: binding.project, Repository: binding.repository, TaskKey: binding.taskKey}
		if binding.claim != taskclaim.Name(id) || binding.claim != claimObjectName(binding.project, binding.repository, binding.taskKey) {
			t.Fatalf("fixture claim for %s was not generated from its canonical identity", task)
		}
		if strings.HasPrefix(binding.claim, "claim-") || binding.claim == claimObjectName(binding.project, binding.repository, "issue:"+task) {
			t.Fatalf("fixture claim for %s was derived from the work-package id", task)
		}
		if !taskclaim.ValidName(binding.claim) || !canonicalClaimName(binding.claim) || !bindingName.MatchString(binding.session) || canonicalClaimName(binding.session) {
			t.Fatalf("claim and session grammars crossed for %s", task)
		}
	}
	identities := []coord.TaskIdentity{
		{Project: "dev-env", Repository: "dev-env", TaskKey: "issue:501"},
		{Project: "dev-env", Repository: "dev-env", TaskKey: "issue:178"},
		{Project: "dev-env", Repository: "other", TaskKey: "issue:501"},
		{Project: "other", Repository: "dev-env", TaskKey: "issue:501"},
		{Project: "dev-env", Repository: "dev-env", TaskKey: "task:attention-120"},
		{Project: "ab", Repository: "cd", TaskKey: "task:a"},
	}
	for _, id := range identities {
		got := claimObjectName(id.Project, id.Repository, id.TaskKey)
		want := taskclaim.Name(id)
		if got != want || len(got) != len(want) || taskclaim.ValidName(got) != canonicalClaimName(got) {
			t.Fatalf("claim name drifted for %s/%s/%s", id.Project, id.Repository, id.TaskKey)
		}
		if claimObjectName(id.Project, id.Repository, id.TaskKey) == claimObjectName(id.Project, id.Repository, id.TaskKey+"-other") {
			t.Fatal("taskKey did not change the claim name")
		}
	}
	if claimObjectName("dev-env", "dev-env", "issue:501") == claimObjectName("other", "dev-env", "issue:501") || claimObjectName("dev-env", "dev-env", "issue:501") == claimObjectName("dev-env", "other", "issue:501") {
		t.Fatal("project or repository was omitted from the claim name")
	}
	sample := taskclaim.Name(coord.TaskIdentity{Project: "dev-env", Repository: "dev-env", TaskKey: "issue:501"})
	for _, name := range []string{
		"claim-178",
		sample[:len(sample)-1],
		strings.ToUpper(sample),
		"task-claim." + strings.Repeat("A", 32) + "." + strings.Repeat("a", 32),
		"task-claim." + strings.Repeat("g", 32) + "." + strings.Repeat("a", 32),
		sample + "/extra",
		sample + "?project=dev-env",
		sample + "\n",
		sample + "\x00",
		"task-claim." + strings.Repeat("ab", 32),
		"",
	} {
		if taskclaim.ValidName(name) != canonicalClaimName(name) || canonicalClaimName(name) {
			t.Fatalf("malformed claim name accepted or grammar drifted: %q", name)
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
