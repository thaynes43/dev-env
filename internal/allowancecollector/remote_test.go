//go:build linux

package allowancecollector

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// Exercise the fixed delivery bootstrap using an existing harmless ELF, never
// a provider/native client. The binary is anonymous, digest-checked and sealed.
func TestAnonymousBootstrapExecutesExactVerifiedELFAndRejectsDigestMismatch(t *testing.T) {
	if _, err := os.Stat("/usr/bin/python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	binary, err := os.ReadFile("/bin/echo")
	if err != nil {
		t.Skip("harmless ELF unavailable")
	}
	for _, valid := range []bool{true, false} {
		sha := digest(binary)
		if !valid {
			sha = "0000000000000000000000000000000000000000000000000000000000000000"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, "/usr/bin/python3", "-c", bootstrap, strconv.Itoa(len(binary)), sha)
		cmd.Stdin = io.MultiReader(bytes.NewReader(binary), bytes.NewReader([]byte("unconsumed request")))
		out, err := cmd.Output()
		cancel()
		if valid && (err != nil || string(out) != "internal-allowance-capture\n") {
			t.Fatal("sealed anonymous execution failed", err)
		}
		if !valid && (err == nil || len(out) != 0) {
			t.Fatal("wrong digest executed")
		}
	}
}
