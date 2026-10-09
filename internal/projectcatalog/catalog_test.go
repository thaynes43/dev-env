package projectcatalog

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

const validCatalog = `{"version":1,"repositories":{"demo":{"github":"owner/demo"},"other":{"github":"owner/other","defaultBranch":"stable"}},"projects":{"sample":{"repositories":[{"name":"other","defaultBranch":"release/one"},{"name":"demo"}],"rules":"Rule identifier α\nKeep this exact trailing space. "}}}`

func TestCatalogExactBytesAndImmutableSnapshots(t *testing.T) {
	c, err := Parse([]byte(validCatalog))
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Snapshot("sample", "other")
	if err != nil {
		t.Fatal(err)
	}
	if c.Revision() != Digest([]byte(validCatalog)) || s.RulesRevision() != Digest([]byte(s.Rules())) {
		t.Fatal("revision did not hash exact bytes")
	}
	if s.Selected().DefaultBranch != "release/one" || c.Repositories()[1].DefaultBranch != "stable" {
		t.Fatal("project default changed global authority")
	}
	copy := s.Repositories()
	copy[0].GitHub = "foreign/repo"
	if s.Repositories()[0].GitHub != "owner/demo" {
		t.Fatal("caller changed immutable snapshot")
	}
	if _, err := c.Snapshot("sample", ""); err == nil {
		t.Fatal("multi-repo task silently selected a repo")
	}
	if _, err := c.Snapshot("sample", "missing"); err == nil {
		t.Fatal("foreign repo selected")
	}
	wrapper := s.ProjectRules()
	if !strings.HasSuffix(wrapper, s.Rules()+"\n") || !strings.Contains(wrapper, "catalog-sha256="+c.Revision()) {
		t.Fatal("wrapper lost exact rules or metadata")
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := ParseSnapshot(data)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ProjectRules() != wrapper {
		t.Fatal("resume changed saved rules")
	}
	c2, err := Parse([]byte(validCatalog + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c2.Revision() == c.Revision() {
		t.Fatal("raw catalog whitespace did not change digest")
	}
}

func TestCatalogRejectsUntrustedShapes(t *testing.T) {
	cases := map[string]string{
		"unknown top":           strings.Replace(validCatalog, `"version":1`, `"version":1,"extra":true`, 1),
		"unknown nested":        strings.Replace(validCatalog, `"github":"owner/demo"`, `"github":"owner/demo","path":"/tmp"`, 1),
		"duplicate top":         strings.Replace(validCatalog, `"version":1`, `"version":1,"version":1`, 1),
		"escaped duplicate":     strings.Replace(validCatalog, `"github":"owner/demo"`, `"github":"owner/demo","\u0067ithub":"owner/demo"`, 1),
		"case alias duplicate":  strings.Replace(validCatalog, `"github":"owner/demo"`, `"github":"owner/demo","GitHub":"foreign/demo"`, 1),
		"single case alias":     strings.Replace(validCatalog, `"defaultBranch":"stable"`, `"defaultbranch":"stable"`, 1),
		"duplicate identity":    strings.Replace(validCatalog, "owner/other", "OWNER/DEMO", 1),
		"credentials":           strings.Replace(validCatalog, "owner/demo", "https://user:pass@github.com/owner/demo", 1),
		"path name":             strings.Replace(validCatalog, `"sample":`, `"../sample":`, 1),
		"wrong branch":          strings.Replace(validCatalog, "release/one", "main~1", 1),
		"empty explicit branch": strings.Replace(validCatalog, `"stable"`, `""`, 1),
		"null optional":         strings.Replace(validCatalog, `"stable"`, `null`, 1),
		"missing rules":         strings.Replace(validCatalog, `,"rules":"Rule identifier α\nKeep this exact trailing space. "`, "", 1),
		"unknown repo":          strings.Replace(validCatalog, `"name":"demo"`, `"name":"absent"`, 1),
		"repeated repo":         strings.Replace(validCatalog, `"name":"other"`, `"name":"demo"`, 1),
		"two documents":         validCatalog + ` {}`,
		"bad version":           strings.Replace(validCatalog, `"version":1`, `"version":2`, 1),
		"oversize":              strings.Repeat(" ", MaxBytes+1),
		"rules oversize":        strings.Replace(validCatalog, `Rule identifier α\nKeep this exact trailing space. `, strings.Repeat("x", MaxRulesBytes+1), 1),
		"invalid utf8":          validCatalog + string([]byte{0xff}),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(input)); err == nil {
				t.Fatal("accepted invalid catalog")
			}
		})
	}
}

func TestCatalogCollectionCaps(t *testing.T) {
	if _, err := Parse([]byte(`{"version":1,"repositories":{},"projects":{}}`)); err != nil {
		t.Fatal("empty accepted catalog cannot report removed projects", err)
	}
	for _, count := range []int{MaxProjects, MaxProjects + 1} {
		var projects []string
		for i := 0; i < count; i++ {
			projects = append(projects, fmt.Sprintf(`"p%d":{"repositories":[{"name":"demo"}],"rules":""}`, i))
		}
		input := `{"version":1,"repositories":{"demo":{"github":"owner/demo"}},"projects":{` + strings.Join(projects, ",") + `}}`
		_, err := Parse([]byte(input))
		if (err == nil) != (count == MaxProjects) {
			t.Fatalf("project bound %d: %v", count, err)
		}
	}
	for _, count := range []int{MaxRepositories, MaxRepositories + 1} {
		var repos []string
		for i := 0; i < count; i++ {
			repos = append(repos, fmt.Sprintf(`"r%d":{"github":"owner/r%d"}`, i, i))
		}
		input := `{"version":1,"repositories":{` + strings.Join(repos, ",") + `},"projects":{"p":{"repositories":[{"name":"r0"}],"rules":""}}}`
		_, err := Parse([]byte(input))
		if (err == nil) != (count == MaxRepositories) {
			t.Fatalf("repo bound %d: %v", count, err)
		}
	}
	for _, count := range []int{MaxProjectRepositories, MaxProjectRepositories + 1} {
		var repos, selected []string
		for i := 0; i < count; i++ {
			repos = append(repos, fmt.Sprintf(`"r%d":{"github":"owner/r%d"}`, i, i))
			selected = append(selected, fmt.Sprintf(`{"name":"r%d"}`, i))
		}
		input := `{"version":1,"repositories":{` + strings.Join(repos, ",") + `},"projects":{"p":{"repositories":[` + strings.Join(selected, ",") + `],"rules":""}}}`
		_, err := Parse([]byte(input))
		if (err == nil) != (count == MaxProjectRepositories) {
			t.Fatalf("selection bound %d: %v", count, err)
		}
	}
}

func TestSavedSnapshotRejectsMissingRulesAndChangedRuleDigest(t *testing.T) {
	c, err := Parse([]byte(validCatalog))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.Snapshot("sample", "demo")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "rules")
	fields["rulesRevision"], err = json.Marshal(Digest(nil))
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSnapshot(data); err == nil {
		t.Fatal("missing rules silently became empty instructions")
	}
	fields["rules"] = json.RawMessage(`"changed"`)
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSnapshot(data); err == nil {
		t.Fatal("saved rules digest mismatch accepted")
	}
	original, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	aliased := strings.Replace(string(original), `"rules":`, `"Rules":"shadowed","rules":`, 1)
	if _, err := ParseSnapshot([]byte(aliased)); err == nil {
		t.Fatal("saved snapshot accepts case-insensitive rule alias")
	}
}

func TestInitialCatalog(t *testing.T) {
	data, err := os.ReadFile("../../config/projects/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.ProjectNames(), []string{"dev-env", "sigo-alumni"}) {
		t.Fatal("unexpected initial project names")
	}
	s, err := c.Snapshot("sigo-alumni", "sigo-alumni")
	if err != nil || len(s.Repositories()) != 3 {
		t.Fatal("initial multi-repo project changed")
	}
}

func TestBranches(t *testing.T) {
	for _, good := range []string{"main", "release/one", "v2.0", "feature-α"} {
		if !ValidBranch(good) {
			t.Errorf("refused %q", good)
		}
	}
	for _, bad := range []string{"", "HEAD", "@", "refs/heads/main", "-main", "main~1", "main^", "main@{1}", "main..old", "a/.b", "main.lock", "main/", "main\n", "main?", "main\\old"} {
		if ValidBranch(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}
