package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestValidateSSHCACommandExitsBeforeClusterSetup(t *testing.T) {
	const helperEnv = "DEV_ENV_TEST_VALIDATE_SSH_CA"
	if os.Getenv(helperEnv) == "true" {
		os.Args = []string{"dev-env-keeper", "validate-ssh-ca", "--ssh-ca-dir=" + os.Getenv("DEV_ENV_TEST_CA_DIR")}
		main()
		return
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, raw := range map[string][]byte{"private-key": pem.EncodeToMemory(block), "public-key": ssh.MarshalAuthorizedKey(public)} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(os.Args[0], "-test.run=^TestValidateSSHCACommandExitsBeforeClusterSetup$")
	// Normal keeper startup would fail with this kubeconfig. The validator must
	// succeed without Kubernetes, App files, targets, host pins or journal access.
	command.Env = append(os.Environ(), helperEnv+"=true", "DEV_ENV_TEST_CA_DIR="+dir, "KUBECONFIG="+filepath.Join(dir, "missing-kubeconfig"))
	raw, err := command.CombinedOutput()
	want := "{\"version\":1,\"privateKeyValid\":true,\"publicKeyValid\":true,\"keysMatch\":true,\"valid\":true,\"failureCode\":\"\"}\n"
	if err != nil || string(raw) != want {
		t.Fatalf("offline command returned %q, error %v", raw, err)
	}
}

func TestValidateSSHCACommandRedactsInvalidArgumentsAndFiles(t *testing.T) {
	for _, args := range [][]string{
		{"--unknown=PRIVATE_ARGUMENT_CANARY"},
		{"--ssh-ca-dir="},
		{"PRIVATE_ARGUMENT_CANARY"},
		{"--ssh-ca-dir=" + filepath.Join(t.TempDir(), "PRIVATE_PATH_CANARY")},
	} {
		var out bytes.Buffer
		if validateSSHCA(args, &out) != 1 {
			t.Fatal("invalid arguments or unavailable files accepted")
		}
		var report map[string]any
		if json.Unmarshal(out.Bytes(), &report) != nil || len(report) != 6 || report["version"] != float64(1) || report["valid"] != false {
			t.Fatal("failure did not emit the fixed schema")
		}
		code, ok := report["failureCode"].(string)
		if !ok || (code != "Unavailable" && code != "InvalidArguments") || bytes.Contains(out.Bytes(), []byte("CANARY")) {
			t.Fatal("failure report exposes argument or path contents")
		}
	}
	if validateSSHCA([]string{"--unknown=CANARY"}, failingValidationWriter{}) != 1 {
		t.Fatal("failed output accepted")
	}
}

type failingValidationWriter struct{}

func (failingValidationWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
