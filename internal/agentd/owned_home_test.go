package agentd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/thaynes43/dev-env/internal/commonrules"
	commonfixture "github.com/thaynes43/dev-env/internal/commonrules/testutil"
)

func ownedHomeFixture(t *testing.T) (Settings, map[string]string, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	snap := commonfixture.Snapshot(t, []byte("public project"), "COMMON_RULE_FIXTURE", "CODEX_HEADER_FIXTURE")
	common, err := snap.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(OwnedHomeInput{Version: 1, CommonRules: common, CodexConfig: "model = 'gpt-6-luna'\nproject_doc_fallback_filenames = ['CLAUDE.md']\nproject_doc_max_bytes = 131072\n"})
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]string{"input.json": string(raw), "requirements.toml": "allowed_approval_policies = ['never']\n"}
	bound, _ := json.Marshal(data)
	return Settings{Home: home, CodexHome: filepath.Join(home, ".codex"), StateDir: filepath.Join(home, ".agentd")}, data, commonrules.Digest(bound)
}
func TestPrepareOwnedHomeComposesRulesBeforeNativeWithoutCredentialInput(t *testing.T) {
	s, data, digest := ownedHomeFixture(t)
	if err := PrepareOwnedHome(s, data, digest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(s.CodexHome, "AGENTS.md"))
	if err != nil || string(got) != "CODEX_HEADER_FIXTURE\nCOMMON_RULE_FIXTURE" {
		t.Fatal("composition", string(got), err)
	}
	history := filepath.Join(s.CodexHome, "retained-history")
	if err = os.WriteFile(history, []byte("opaque retained fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = PrepareOwnedHome(s, data, digest); err != nil {
		t.Fatal("identical preparation must preserve retained data", err)
	}
	if got, err = os.ReadFile(history); err != nil || string(got) != "opaque retained fixture" {
		t.Fatal("retained data changed", err)
	}
	for _, path := range []string{filepath.Join(s.CodexHome, "auth.json"), filepath.Join(s.StateDir, "owned-budget-attempt"), filepath.Join(s.StateDir, "owned-host.json")} {
		if _, err = os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("preparation created credential/authority", path, err)
		}
	}
}
func TestPrepareOwnedHomeRefusesChangedOrAliasedInputsWithoutOverwrite(t *testing.T) {
	for _, scenario := range []string{"digest", "extra-input", "invalid-toml", "conflicting-config", "symlink", "hardlink", "wrong-home", "wrong-common"} {
		t.Run(scenario, func(t *testing.T) {
			s, data, digest := ownedHomeFixture(t)
			switch scenario {
			case "digest":
				digest = commonrules.Digest([]byte("wrong"))
			case "extra-input":
				data["auth.json"] = "forbidden input"
			case "invalid-toml":
				data["requirements.toml"] = "["
				raw, _ := json.Marshal(data)
				digest = commonrules.Digest(raw)
			case "wrong-common":
				var i OwnedHomeInput
				_ = json.Unmarshal([]byte(data["input.json"]), &i)
				i.CommonRules = json.RawMessage(`{}`)
				raw, _ := json.Marshal(i)
				data["input.json"] = string(raw)
				raw, _ = json.Marshal(data)
				digest = commonrules.Digest(raw)
			case "wrong-home":
				s.CodexHome = filepath.Join(t.TempDir(), "elsewhere")
			default:
				if err := PrepareOwnedHome(s, data, digest); err != nil {
					t.Fatal(err)
				}
				p := filepath.Join(s.CodexHome, "config.toml")
				if scenario == "conflicting-config" {
					if err := os.WriteFile(p, []byte("owner edits"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					outside := filepath.Join(t.TempDir(), "retained")
					if err := os.WriteFile(outside, []byte("do not alter"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(p); err != nil {
						t.Fatal(err)
					}
					var err error
					if scenario == "symlink" {
						err = os.Symlink(outside, p)
					} else {
						err = os.Link(outside, p)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := PrepareOwnedHome(s, data, digest); err == nil {
				t.Fatal("unsafe preparation accepted")
			}
			if scenario == "conflicting-config" {
				raw, err := os.ReadFile(filepath.Join(s.CodexHome, "config.toml"))
				if err != nil || string(raw) != "owner edits" {
					t.Fatal("owner edit overwritten", err)
				}
			}
		})
	}
}

func TestCheckOwnedHomeRefusesDriftAndNeverRepairs(t *testing.T) {
	for _, name := range []string{"config.toml", "AGENTS.md", "app-server-daemon/settings.json"} {
		for _, missing := range []bool{false, true} {
			t.Run(name+fmt.Sprint(missing), func(t *testing.T) {
				s, data, digest := ownedHomeFixture(t)
				if err := CheckOwnedHome(s, data, digest); err == nil {
					t.Fatal("missing HOME accepted")
				}
				if _, err := os.Stat(s.Home); !os.IsNotExist(err) {
					t.Fatal("check created HOME")
				}
				if err := PrepareOwnedHome(s, data, digest); err != nil {
					t.Fatal(err)
				}
				if err := CheckOwnedHome(s, data, digest); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(s.CodexHome, name)
				if missing {
					_ = os.Remove(path)
				} else {
					_ = os.WriteFile(path, []byte("owner changed startup input"), 0600)
				}
				if err := CheckOwnedHome(s, data, digest); err == nil {
					t.Fatal("startup drift accepted")
				}
				got, err := os.ReadFile(path)
				if missing && !os.IsNotExist(err) || !missing && (err != nil || string(got) != "owner changed startup input") {
					t.Fatal("check repaired startup input")
				}
			})
		}
	}
}
