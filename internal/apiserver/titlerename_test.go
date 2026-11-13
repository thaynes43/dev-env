package apiserver

import "testing"

func mustTitleRename(t *testing.T, raw string) TitleRename {
	t.Helper()
	got, err := ParseTitleRenameSessions(raw)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestParseTitleRenameSessions(t *testing.T) {
	empty, err := ParseTitleRenameSessions("")
	if err != nil || empty.Allows("dev-env-1019-120000") || empty.all {
		t.Fatalf("empty %+v %v", empty, err)
	}
	one, err := ParseTitleRenameSessions("dev-env-1019-120000")
	if err != nil || !one.Allows("dev-env-1019-120000") || one.Allows("other") {
		t.Fatalf("one %+v %v", one, err)
	}
	two, err := ParseTitleRenameSessions("dev-env-1019-120000,dev-env-1019-130000")
	if err != nil || !two.Allows("dev-env-1019-120000") || !two.Allows("dev-env-1019-130000") || two.Allows("other") {
		t.Fatalf("two %+v %v", two, err)
	}
	all, err := ParseTitleRenameSessions("*")
	if err != nil || !all.Allows("anything") || !all.all {
		t.Fatalf("star %+v %v", all, err)
	}
	for _, raw := range []string{"a,,b", "a, b", "dev-env-1019-120000,*", "Bad_Name", "dev-env-1019-120000,dev-env-1019-120000", " "} {
		if _, err := ParseTitleRenameSessions(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
