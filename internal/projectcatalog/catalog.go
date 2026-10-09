// Package projectcatalog validates the bounded GitOps project declaration. It
// deliberately has no filesystem, network, provider or management authority.
package projectcatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	MaxBytes               = 256 << 10
	MaxProjects            = 64
	MaxRepositories        = 128
	MaxProjectRepositories = 16
	MaxRulesBytes          = 16 << 10
)

var (
	namePattern       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	ownerPattern      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type declaration struct {
	Version      int                              `json:"version"`
	Repositories map[string]repositoryDeclaration `json:"repositories"`
	Projects     map[string]projectDeclaration    `json:"projects"`
}
type repositoryDeclaration struct {
	GitHub        string  `json:"github"`
	DefaultBranch *string `json:"defaultBranch,omitempty"`
}
type projectDeclaration struct {
	Repositories []selection `json:"repositories"`
	Rules        *string     `json:"rules"`
}
type selection struct {
	Name          string  `json:"name"`
	DefaultBranch *string `json:"defaultBranch,omitempty"`
}

// Repository is a value copy of an accepted identity and resolved default.
type Repository struct {
	Name          string `json:"name"`
	GitHub        string `json:"github"`
	DefaultBranch string `json:"defaultBranch"`
}

func (r Repository) URL() string { return "https://github.com/" + r.GitHub }

// Catalog exposes only value copies; a caller cannot change accepted authority.
type Catalog struct {
	revision     string
	repositories map[string]Repository
	projects     map[string]snapshotDocument
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Parse accepts exactly one UTF-8 JSON document, rejects duplicate keys before
// decoding, and hashes the original bytes rather than a normalized encoding.
func Parse(data []byte) (*Catalog, error) {
	if len(data) == 0 || len(data) > MaxBytes || !utf8.Valid(data) {
		return nil, errors.New("catalog must be a bounded UTF-8 JSON document")
	}
	var d declaration
	if err := decodeStrict(data, &d); err != nil {
		return nil, err
	}
	if d.Version != 1 || d.Repositories == nil || len(d.Repositories) > MaxRepositories || d.Projects == nil || len(d.Projects) > MaxProjects {
		return nil, errors.New("catalog has an unsupported version or invalid collection size")
	}
	c := &Catalog{revision: Digest(data), repositories: map[string]Repository{}, projects: map[string]snapshotDocument{}}
	identities := map[string]bool{}
	for name, repo := range d.Repositories {
		if !ValidName(name) || !validGitHub(repo.GitHub) || identities[strings.ToLower(repo.GitHub)] {
			return nil, fmt.Errorf("invalid or duplicate repository identity for %q", name)
		}
		branch := "main"
		if repo.DefaultBranch != nil {
			branch = *repo.DefaultBranch
		}
		if !ValidBranch(branch) {
			return nil, fmt.Errorf("invalid repository default for %q", name)
		}
		identities[strings.ToLower(repo.GitHub)] = true
		c.repositories[name] = Repository{Name: name, GitHub: repo.GitHub, DefaultBranch: branch}
	}
	for name, project := range d.Projects {
		if !ValidName(name) || project.Rules == nil || len(*project.Rules) > MaxRulesBytes || len(project.Repositories) == 0 || len(project.Repositories) > MaxProjectRepositories {
			return nil, fmt.Errorf("invalid project %q", name)
		}
		doc := snapshotDocument{Version: 1, Project: name, CatalogRevision: c.revision, RulesRevision: Digest([]byte(*project.Rules)), Rules: *project.Rules}
		seen := map[string]bool{}
		for _, selected := range project.Repositories {
			repo, ok := c.repositories[selected.Name]
			if !ok || seen[selected.Name] {
				return nil, fmt.Errorf("unknown or repeated project repository in %q", name)
			}
			seen[selected.Name] = true
			if selected.DefaultBranch != nil {
				if !ValidBranch(*selected.DefaultBranch) {
					return nil, fmt.Errorf("invalid project default in %q", name)
				}
				repo.DefaultBranch = *selected.DefaultBranch
			}
			doc.Repositories = append(doc.Repositories, repo)
		}
		slices.SortFunc(doc.Repositories, func(a, b Repository) int { return strings.Compare(a.Name, b.Name) })
		c.projects[name] = doc
	}
	return c, nil
}

func ValidName(name string) bool { return namePattern.MatchString(name) }
func validGitHub(identity string) bool {
	owner, repo, ok := strings.Cut(identity, "/")
	return ok && ownerPattern.MatchString(owner) && !strings.Contains(owner, "--") && repositoryPattern.MatchString(repo) && repo != "." && repo != ".." && !strings.HasSuffix(strings.ToLower(repo), ".git")
}

// ValidBranch mirrors Git's branch ref restrictions and rejects revision
// expressions and pseudo-refs. It is independent of local Git configuration.
func ValidBranch(branch string) bool {
	if branch == "" || !utf8.ValidString(branch) || len(branch) > 255 || branch == "HEAD" || branch == "@" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "refs/") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.ContainsAny(branch, " ~^:?*[\\") {
		return false
	}
	for _, ch := range branch {
		if ch < 0x20 || ch == 0x7f {
			return false
		}
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func (c *Catalog) Revision() string { return c.revision }
func (c *Catalog) ProjectNames() []string {
	names := make([]string, 0, len(c.projects))
	for name := range c.projects {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
func (c *Catalog) Repositories() []Repository {
	repos := make([]Repository, 0, len(c.repositories))
	for _, repo := range c.repositories {
		repos = append(repos, repo)
	}
	slices.SortFunc(repos, func(a, b Repository) int { return strings.Compare(a.Name, b.Name) })
	return repos
}

type snapshotDocument struct {
	Version         int          `json:"version"`
	Project         string       `json:"project"`
	CatalogRevision string       `json:"catalogRevision"`
	RulesRevision   string       `json:"rulesRevision"`
	Rules           string       `json:"rules"`
	Repositories    []Repository `json:"repositories"`
	Selected        string       `json:"selected,omitempty"`
}

// Snapshot is an immutable copy resolved by the server's accepted Catalog.
// It must be stored in private platform state, never in a repository worktree.
type Snapshot struct{ document snapshotDocument }

// ProjectSnapshot is for materialization, which includes every declared repo.
// Its deterministic selection is not authority to start a multi-repo task.
func (c *Catalog) ProjectSnapshot(project string) (Snapshot, error) {
	doc, ok := c.projects[project]
	if !ok {
		return Snapshot{}, errors.New("project is not in the accepted catalog")
	}
	return c.Snapshot(project, doc.Repositories[0].Name)
}

func (c *Catalog) Snapshot(project, selected string) (Snapshot, error) {
	doc, ok := c.projects[project]
	if !ok {
		return Snapshot{}, errors.New("project is not in the accepted catalog")
	}
	if selected == "" && len(doc.Repositories) == 1 {
		selected = doc.Repositories[0].Name
	}
	if selected != "" {
		if !slices.ContainsFunc(doc.Repositories, func(repo Repository) bool { return repo.Name == selected }) {
			return Snapshot{}, errors.New("selected repository is not in the project")
		}
	} else {
		return Snapshot{}, errors.New("multi-repository tasks require an explicit repository")
	}
	doc.Selected = selected
	doc.Repositories = slices.Clone(doc.Repositories)
	return Snapshot{document: doc}, nil
}
func (s Snapshot) Project() string            { return s.document.Project }
func (s Snapshot) CatalogRevision() string    { return s.document.CatalogRevision }
func (s Snapshot) RulesRevision() string      { return s.document.RulesRevision }
func (s Snapshot) Rules() string              { return s.document.Rules }
func (s Snapshot) Repositories() []Repository { return slices.Clone(s.document.Repositories) }
func (s Snapshot) Selected() Repository {
	for _, repo := range s.document.Repositories {
		if repo.Name == s.document.Selected {
			return repo
		}
	}
	return Repository{}
}
func (s Snapshot) MarshalJSON() ([]byte, error) { return json.Marshal(s.document) }

// ParseSnapshot validates a saved private receipt for resume. It cannot replace
// server catalog resolution for a new task.
func ParseSnapshot(data []byte) (Snapshot, error) {
	if len(data) > 64<<10 || !utf8.Valid(data) {
		return Snapshot{}, errors.New("invalid snapshot size or encoding")
	}
	var doc snapshotDocument
	if err := decodeStrict(data, &doc); err != nil {
		return Snapshot{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Snapshot{}, err
	}
	for _, required := range []string{"version", "project", "catalogRevision", "rulesRevision", "rules", "repositories", "selected"} {
		if _, ok := fields[required]; !ok {
			return Snapshot{}, errors.New("project snapshot is missing a required field")
		}
	}
	if doc.Version != 1 || !ValidName(doc.Project) || !digestPattern.MatchString(doc.CatalogRevision) || doc.RulesRevision != Digest([]byte(doc.Rules)) || len(doc.Rules) > MaxRulesBytes || len(doc.Repositories) == 0 || len(doc.Repositories) > MaxProjectRepositories {
		return Snapshot{}, errors.New("invalid project snapshot")
	}
	seen := map[string]bool{}
	for _, repo := range doc.Repositories {
		if !ValidName(repo.Name) || !validGitHub(repo.GitHub) || !ValidBranch(repo.DefaultBranch) || seen[repo.Name] {
			return Snapshot{}, errors.New("invalid snapshot repository")
		}
		seen[repo.Name] = true
	}
	if !seen[doc.Selected] {
		return Snapshot{}, errors.New("snapshot has no selected repository")
	}
	return Snapshot{document: doc}, nil
}

// ProjectRules is the driver-ratified wrapper, with exact rules bytes preserved.
func (s Snapshot) ProjectRules() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Project %s\n\nThis is the permanent %s project. Repository anchors are read-only. Start implementation through the managed task launcher, which fetches and pins source.\n\nProject repositories:\n", s.Project(), s.Project())
	for _, repo := range s.Repositories() {
		fmt.Fprintf(&b, "- %s -> %s (default branch: %s)\n", repo.Name, repo.Name, repo.DefaultBranch)
	}
	fmt.Fprintf(&b, "\n<!-- dev-env-project catalog-sha256=%s rules-sha256=%s -->\n\n## Project rules\n\n", s.CatalogRevision(), s.RulesRevision())
	b.WriteString(s.Rules())
	b.WriteByte('\n')
	return b.String()
}

func decodeStrict(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if err := rejectDuplicateKeys(d); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON must contain exactly one document")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

// Walk JSON tokens recursively before struct decoding (which otherwise accepts
// duplicate object keys, including escaped spellings of the same key).
func rejectDuplicateKeys(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("null JSON fields are not accepted")
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := token.(string)
			if !ok || keys[key] {
				return errors.New("duplicate JSON object key")
			}
			keys[key] = true
			if err := rejectDuplicateKeys(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := rejectDuplicateKeys(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	_, err = d.Token()
	return err
}
