package session

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

const (
	goTestStepName = "Focused actual PostgreSQL and authorization tests"
	proofEnvName   = "PORTAL_REQUIRE_DISPOSABLE_PROOF"
	workflowPath   = "../../../.github/workflows/portal-session-postgres.yml"
)

// d210Wiring is the published defect: the proof variable exists only on the TLS
// setup step. GitHub Actions does not copy that step's environment into the
// later go-test process.
const d210Wiring = `
jobs:
  postgres:
    steps:
      - name: Enable disposable verified TLS and role-scoped HBA
        env:
          PORTAL_POSTGRES_CONTAINER: container
          PORTAL_REQUIRE_DISPOSABLE_PROOF: "1"
        run: echo setup
      - name: Focused actual PostgreSQL and authorization tests
        env:
          GOTOOLCHAIN: local
          GOMAXPROCS: "2"
          PORTAL_POSTGRES_CONTAINER: container
        run: go test ./web/portal/session
`

const jobLevelProof = `
jobs:
  postgres:
    env:
      PORTAL_REQUIRE_DISPOSABLE_PROOF: "1"
    steps:
      - name: Enable disposable verified TLS and role-scoped HBA
        env:
          PORTAL_POSTGRES_CONTAINER: container
        run: echo setup
      - name: Focused actual PostgreSQL and authorization tests
        env:
          GOTOOLCHAIN: local
        run: go test ./web/portal/session
`

type ghaWorkflow struct {
	Jobs map[string]ghaJob `json:"jobs"`
}

type ghaJob struct {
	Env   map[string]string `json:"env"`
	Steps []ghaStep         `json:"steps"`
}

type ghaStep struct {
	Name string            `json:"name"`
	Env  map[string]string `json:"env"`
	Run  string            `json:"run"`
}

// goTestProcessEnv is the environment GitHub gives the go-test step: job env,
// then that step's own env. Earlier steps do not contribute, and the run script
// is not treated as an export.
func goTestProcessEnv(doc []byte) (map[string]string, error) {
	var wf ghaWorkflow
	if err := yaml.Unmarshal(doc, &wf); err != nil || len(wf.Jobs) == 0 {
		return nil, errors.New("workflow has no jobs")
	}
	jobName := ""
	stepIndex := -1
	for name, job := range wf.Jobs {
		for i, step := range job.Steps {
			if step.Name != goTestStepName {
				continue
			}
			if stepIndex >= 0 {
				return nil, errors.New("more than one go-test step")
			}
			jobName = name
			stepIndex = i
		}
	}
	if stepIndex < 0 {
		return nil, errors.New("go-test step missing")
	}
	job := wf.Jobs[jobName]
	effective := make(map[string]string, len(job.Env)+len(job.Steps[stepIndex].Env))
	for key, value := range job.Env {
		effective[key] = value
	}
	for key, value := range job.Steps[stepIndex].Env {
		effective[key] = value
	}
	return effective, nil
}

func requireProofOnGoTestProcess(doc []byte) error {
	env, err := goTestProcessEnv(doc)
	if err != nil {
		return err
	}
	if env[proofEnvName] != "1" {
		return fmt.Errorf("go test process %s=%q", proofEnvName, env[proofEnvName])
	}
	return nil
}

func TestWorkflowProofReachesGoTestProcess(t *testing.T) {
	if err := requireProofOnGoTestProcess([]byte(d210Wiring)); err == nil {
		t.Fatal("d210 wiring accepted")
	}
	if err := requireProofOnGoTestProcess([]byte(jobLevelProof)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Clean(workflowPath)
	doc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	env, err := goTestProcessEnv(doc)
	if err != nil {
		t.Fatal(err)
	}
	if env[proofEnvName] != "1" {
		t.Fatalf("checked-in go test process %s=%q", proofEnvName, env[proofEnvName])
	}
	// The run script is not an acceptable substitute for the process env.
	if _, ok := env["PORTAL_TEST_DATABASE_URL"]; ok {
		t.Fatal("database URL must be exported by the step script, not inherited as process env from another step")
	}
}

func TestRequireModeMissingInputsFail(t *testing.T) {
	if os.Getenv("PORTAL_PROOF_CHILD") == "failover" {
		requireHostedProof(t, hostedFailoverInputMissing(), "hosted disposable physical failover not configured; no failover proof")
		t.Fatal("missing failover input was accepted")
	}
	database := runProofChild(t, []string{"-test.run=^TestPostgresIntegration$", "-test.count=1", "-test.v"}, "PORTAL_REQUIRE_DISPOSABLE_PROOF=1")
	if database.err == nil || !bytes.Contains(database.out, []byte("required hosted proof missing: disposable PostgreSQL not configured; no DB proof")) {
		t.Fatalf("missing database did not fail closed: %v\n%s", database.err, database.out)
	}
	failover := runProofChild(t, []string{"-test.run=^TestRequireModeMissingInputsFail$", "-test.count=1", "-test.v"}, "PORTAL_REQUIRE_DISPOSABLE_PROOF=1", "PORTAL_PROOF_CHILD=failover")
	if failover.err == nil || !bytes.Contains(failover.out, []byte("required hosted proof missing: hosted disposable physical failover not configured; no failover proof")) {
		t.Fatalf("missing failover input did not fail closed: %v\n%s", failover.err, failover.out)
	}
}

type proofChild struct {
	out []byte
	err error
}

func runProofChild(t *testing.T, args []string, extra ...string) proofChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = proofChildEnv(extra...)
	out, err := cmd.CombinedOutput()
	return proofChild{out: out, err: err}
}

func proofChildEnv(extra ...string) []string {
	env := make([]string, 0, 12+len(extra))
	for _, key := range []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "GOCACHE", "GOROOT", "GOPATH", "GOTOOLCHAIN", "GOMAXPROCS", "GODEBUG"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return append(env, extra...)
}
