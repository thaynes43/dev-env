package tools

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const (
	captureMsg = "claude-statusline-usage: capture unavailable\n"
	usageMsg   = "claude-statusline-usage: expected --output PATH [--display CMD ARG...]\n"
	canary     = "SECRET-CANARY"
)

func toolsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Dir(file)
}

const consumerLiteral = "/usr/local/bin/agent-run"

var (
	pairOnce      sync.Once
	helperScript  string
	builtAgentRun string
	siblingMarker string
	pairErr       error
)

func withoutEnv(base []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(base))
	for _, item := range base {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return out
}

func futureReset() string {
	return strconv.FormatInt(time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC).Unix(), 10)
}

func statuslinePair(t *testing.T) {
	t.Helper()
	pairOnce.Do(func() {
		goBin, err := exec.LookPath("go")
		if err != nil {
			pairErr = err
			return
		}
		pairDir, err := os.MkdirTemp("", "claude-statusline-pair-")
		if err != nil {
			pairErr = err
			return
		}
		buildDir := filepath.Join(pairDir, "build")
		scriptDir := filepath.Join(pairDir, "script")
		if err = os.MkdirAll(buildDir, 0o700); err != nil {
			pairErr = err
			return
		}
		if err = os.MkdirAll(scriptDir, 0o700); err != nil {
			pairErr = err
			return
		}
		builtAgentRun = filepath.Join(buildDir, "agent-run")
		cmd := exec.Command("nice", "-n", "19", goBin, "build", "-o", builtAgentRun, "./cmd/agent-run")
		cmd.Dir = filepath.Clean(filepath.Join(toolsDir(t), "..", "..", ".."))
		cmd.Env = append(withoutEnv(withoutEnv(withoutEnv(os.Environ(), "GOMAXPROCS"), "GOFLAGS"), "CGO_ENABLED"), "GOMAXPROCS=1", "GOFLAGS=-p=1", "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			pairErr = fmt.Errorf("build agent-run: %w %s", err, out)
			return
		}
		src, err := os.ReadFile(filepath.Join(toolsDir(t), "claude-statusline-usage.py"))
		if err != nil {
			pairErr = err
			return
		}
		if !bytes.Contains(src, []byte(consumerLiteral)) {
			pairErr = errors.New("consumer literal missing")
			return
		}
		replaced := bytes.ReplaceAll(src, []byte(consumerLiteral), []byte(builtAgentRun))
		helperScript = filepath.Join(scriptDir, "claude-statusline-usage.py")
		if err = os.WriteFile(helperScript, replaced, 0o755); err != nil {
			pairErr = err
			return
		}
		siblingMarker = filepath.Join(pairDir, "sibling-marker")
		stub := "#!/bin/sh\nprintf sibling >> '" + siblingMarker + "'\nexit 0\n"
		if err = os.WriteFile(filepath.Join(scriptDir, "agent-run"), []byte(stub), 0o700); err != nil {
			pairErr = err
		}
	})
	if pairErr != nil {
		t.Fatal(pairErr)
	}
}

type helperResult struct {
	stdout string
	stderr string
	code   int
}

func runHelper(t *testing.T, home, output string, payload []byte, display []string, extra []string) helperResult {
	t.Helper()
	statuslinePair(t)
	script := helperScript
	args := []string{script, "--output", output}
	if display != nil {
		args = append(append(args, "--display"), display...)
	}
	cmd := exec.Command("python3", args...)
	cmd.Dir = home
	cmd.Env = append([]string{
		"HOME=" + home,
		"TMPDIR=" + home,
		"PATH=/usr/bin:/bin",
		"LANG=C",
		"LC_ALL=C",
		"PYTHONNOUSERSITE=1",
		"PYTHONDONTWRITEBYTECODE=1",
	}, extra...)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	return helperResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func privatePaths(t *testing.T) (home, cache, file string) {
	t.Helper()
	home = t.TempDir()
	cache = filepath.Join(home, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(home, 0o700); err != nil || os.Chmod(cache, 0o700) != nil {
		t.Fatal("chmod")
	}
	return home, cache, filepath.Join(cache, "read.json")
}

func writePrior(t *testing.T, file string) []byte {
	t.Helper()
	prior := []byte("PRIOR-VALID\n")
	if err := os.WriteFile(file, prior, 0o600); err != nil {
		t.Fatal(err)
	}
	return prior
}

func assertSame(t *testing.T, file string, prior []byte) {
	t.Helper()
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, prior) {
		t.Fatal("cache changed")
	}
}

func assertNoTemps(t *testing.T, cache string) {
	t.Helper()
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".csu-") {
			t.Fatal("temp left behind")
		}
	}
}

func assertNoLeak(t *testing.T, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if strings.Contains(part, canary) || strings.Contains(part, "total_cost") || strings.Contains(part, "keep-me") {
			t.Fatal("sensitive metadata leaked")
		}
	}
}

func decodeCache(t *testing.T, file string) providerallowance.ReadResponse {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func positiveBody() []byte {
	return []byte(`{"cwd":"SECRET-CANARY-CWD","session_id":"SECRET-CANARY-SESSION","transcript_path":"SECRET-CANARY-PATH","model":{"id":"SECRET-CANARY-MODEL"},"cost":{"total_cost_usd":123},"credential":"SECRET-CANARY-CRED","rate_limits":{"five_hour":{"used_percentage":0,"resets_at":` + futureReset() + `,"extra":"SECRET-CANARY-EXTRA"},"seven_day":{"used_percentage":71.5,"resets_at":1000000000}}}`)
}

func assertWindows(t *testing.T, doc providerallowance.ReadResponse, five, seven string, fiveState, sevenState providerallowance.CurrentState) {
	t.Helper()
	if len(doc.Items) != 1 || len(doc.Items[0].Observation.Measures) != 2 || len(doc.Items[0].CurrentMeasures) != 2 {
		t.Fatal("windows")
	}
	item := doc.Items[0]
	if item.Observation.ProviderObservedAt != nil || item.Freshness.State != providerallowance.FreshUnknown || item.Binding.State != providerallowance.BindingUnknown || item.Binding.AccountRef != nil || item.Observation.Source.Version != "unknown" || item.Observation.Scope.AccountRef != nil {
		t.Fatal("provenance")
	}
	check := func(i int, want string, state providerallowance.CurrentState) {
		t.Helper()
		measure := item.Observation.Measures[i]
		if want == "" {
			if measure.Value.State != providerallowance.ValueUnknown || measure.Value.Decimal != nil {
				t.Fatal("missing became known")
			}
		} else if measure.Value.Decimal == nil || *measure.Value.Decimal != want || measure.Value.State != providerallowance.ValueKnown {
			t.Fatal("decimal")
		}
		if item.CurrentMeasures[i].State != state || item.CurrentMeasures[i].Value.State != providerallowance.ValueUnknown || item.CurrentMeasures[i].Value.Decimal != nil {
			t.Fatal("current")
		}
	}
	check(0, five, fiveState)
	check(1, seven, sevenState)
}

func TestClaudeStatuslinePositiveWindows(t *testing.T) {
	home, cache, file := privatePaths(t)
	secret := filepath.Join(home, "transcript")
	if err := os.WriteFile(secret, []byte("SECRET-CANARY-FILE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	settingsDir := filepath.Join(home, ".claude")
	if err := os.Mkdir(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(settingsDir, "settings.json")
	settingsBody := []byte("{\"statusLine\":{\"command\":\"keep-me\"}}\n")
	if err := os.WriteFile(settings, settingsBody, 0o600); err != nil {
		t.Fatal(err)
	}
	body := bytes.Replace(positiveBody(), []byte("SECRET-CANARY-PATH"), []byte(secret), 1)
	res := runHelper(t, home, file, body, nil, nil)
	if res.code != 0 || res.stdout != "" || res.stderr != "" {
		t.Fatalf("positive code %d stderr %q", res.code, res.stderr)
	}
	assertNoLeak(t, res.stdout, res.stderr)
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	assertNoLeak(t, string(raw))
	if !bytes.Contains(raw, []byte(`"schema":"providerallowance.read.v1"`)) || bytes.Contains(raw, []byte("rate_limits")) {
		t.Fatal("cache shape")
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("mode")
	}
	doc := decodeCache(t, file)
	assertWindows(t, doc, "0", "71.5", providerallowance.CurrentUnknown, providerallowance.CurrentExpired)
	if doc.Items[0].Observation.Measures[0].Window.ResetAt == nil || *doc.Items[0].Observation.Measures[0].Window.ResetAt != time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339) {
		t.Fatal("future reset")
	}
	if doc.Items[0].Observation.Measures[1].Window.ResetAt == nil || *doc.Items[0].Observation.Measures[1].Window.ResetAt != time.Unix(1000000000, 0).UTC().Format(time.RFC3339) {
		t.Fatal("passed reset")
	}
	gotSecret, err := os.ReadFile(secret)
	if err != nil || string(gotSecret) != "SECRET-CANARY-FILE\n" {
		t.Fatal("transcript touched")
	}
	if raw, err := os.ReadFile(siblingMarker); err == nil && len(raw) != 0 {
		t.Fatal("unrelated sibling executable ran")
	}
	gotSettings, err := os.ReadFile(settings)
	if err != nil || !bytes.Equal(gotSettings, settingsBody) {
		t.Fatal("settings touched")
	}
	assertNoTemps(t, cache)
}

func TestClaudeStatuslineZeroNullMissingAndPassedReset(t *testing.T) {
	home, _, file := privatePaths(t)
	body := []byte(`{"rate_limits":{"five_hour":{"used_percentage":0,"resets_at":` + futureReset() + `},"seven_day":{"used_percentage":null,"resets_at":1000000000}}}`)
	if res := runHelper(t, home, file, body, nil, nil); res.code != 0 {
		t.Fatal(res.code)
	}
	doc := decodeCache(t, file)
	assertWindows(t, doc, "0", "", providerallowance.CurrentUnknown, providerallowance.CurrentExpired)
	if doc.Items[0].Observation.Measures[1].Window.ResetAt == nil {
		t.Fatal("passed reset dropped")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if res := runHelper(t, home, file, []byte(`{"rate_limits":{"five_hour":null,"seven_day":{"used_percentage":null,"resets_at":null}}}`), nil, nil); res.code != 0 {
		t.Fatal("null")
	}
	doc = decodeCache(t, file)
	assertWindows(t, doc, "", "", providerallowance.CurrentUnknown, providerallowance.CurrentUnknown)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if res := runHelper(t, home, file, []byte(`{"cwd":"SECRET-CANARY-CWD","model":"SECRET-CANARY-MODEL"}`), nil, nil); res.code != 0 {
		t.Fatal("extra only")
	}
	raw, _ := os.ReadFile(file)
	assertNoLeak(t, string(raw))
	doc = decodeCache(t, file)
	assertWindows(t, doc, "", "", providerallowance.CurrentUnknown, providerallowance.CurrentUnknown)
}

func TestClaudeStatuslineCachedReadKeepsTimes(t *testing.T) {
	home, _, file := privatePaths(t)
	body := []byte(`{"rate_limits":{"five_hour":{"used_percentage":0,"resets_at":` + futureReset() + `},"seven_day":{"used_percentage":0,"resets_at":1000000000}}}`)
	if res := runHelper(t, home, file, body, nil, nil); res.code != 0 {
		t.Fatal(res.code)
	}
	doc := decodeCache(t, file)
	assertWindows(t, doc, "0", "0", providerallowance.CurrentUnknown, providerallowance.CurrentExpired)
	statuslinePair(t)
	bin := builtAgentRun
	read := func() providerallowance.ReadResponse {
		t.Helper()
		cmd := exec.Command(bin, "usage", "-o", "json", "--from-file", file)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home, "LANG=C"}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil || stderr.Len() != 0 {
			t.Fatal(err, stderr.String())
		}
		decoded, err := providerallowance.DecodeReadResponse(stdout.Bytes(), providerallowance.FixtureParseLimits)
		if err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	first, second := read(), read()
	same := func(a, b providerallowance.ReadResponse) {
		t.Helper()
		if a.ServedAt != b.ServedAt || a.Items[0].ReceivedAt != b.Items[0].ReceivedAt || a.Items[0].Observation.CapturedAt != b.Items[0].Observation.CapturedAt || a.Items[0].Freshness.AsOf != b.Items[0].Freshness.AsOf || a.Items[0].Observation.ObservationID != b.Items[0].Observation.ObservationID {
			t.Fatal("cached times changed")
		}
	}
	same(doc, first)
	same(first, second)
	assertWindows(t, second, "0", "0", providerallowance.CurrentUnknown, providerallowance.CurrentExpired)
	side := file + ".side"
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(side, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if res := runHelper(t, home, file, []byte(`{"rate_limits":{"five_hour":{"used_percentage":1,"resets_at":`+futureReset()+`}}}`), nil, nil); res.code != 0 {
		t.Fatal("second capture")
	}
	kept, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	same(doc, kept)
	again := decodeCache(t, side)
	same(doc, again)
}

func TestClaudeStatuslineRefusals(t *testing.T) {
	home, cache, file := privatePaths(t)
	prior := writePrior(t, file)
	cases := []struct {
		name string
		body string
	}{
		{"truncated", "{"},
		{"duplicate", `{"rate_limits":{"five_hour":{"used_percentage":1},"five_hour":{}}}`},
		{"escaped duplicate", `{"rate_limits":{"five_hour":{"used_percentage":0},"\u0066ive_hour":{}}}`},
		{"root case", `{"cwd":"SECRET-CANARY-CWD","Rate_limits":{"five_hour":{"used_percentage":1}}}`},
		{"window case", `{"rate_limits":{"Five_Hour":{"used_percentage":1}}}`},
		{"field case", `{"rate_limits":{"five_hour":{"Used_Percentage":1}}}`},
		{"long s", "{\"rate_limits\":{\"five_hour\":{\"u\\u017fed_percentage\":1}}}"},
		{"over percent", `{"rate_limits":{"five_hour":{"used_percentage":101}}}`},
		{"bool", `{"credential":"SECRET-CANARY-CRED","rate_limits":{"five_hour":{"used_percentage":true}}}`},
		{"fractional reset", `{"rate_limits":{"five_hour":{"resets_at":1.2}}}`},
		{"huge reset", `{"rate_limits":{"five_hour":{"resets_at":999999999999999999999}}}`},
		{"trailing", `{"rate_limits":{}}{"rate_limits":{}}`},
		{"string percent", `{"rate_limits":{"five_hour":{"used_percentage":"SECRET-CANARY-CRED"}}}`},
	}
	for _, tc := range cases {
		res := runHelper(t, home, file, []byte(tc.body), nil, nil)
		if res.code == 0 || res.stdout != "" || res.stderr != captureMsg {
			t.Fatalf("%s accepted", tc.name)
		}
		assertNoLeak(t, res.stdout, res.stderr)
		assertSame(t, file, prior)
	}
	res := runHelper(t, home, file, append([]byte{0xef, 0xbb, 0xbf}, []byte(`{"rate_limits":{}}`)...), nil, nil)
	if res.code == 0 || res.stderr != captureMsg {
		t.Fatal("bom")
	}
	assertSame(t, file, prior)
	res = runHelper(t, home, file, []byte{0xff, 0xfe}, nil, nil)
	if res.code == 0 || strings.Contains(res.stderr, canary) {
		t.Fatal("binary")
	}
	assertSame(t, file, prior)
	over := append([]byte("SECRET-CANARY"), bytes.Repeat([]byte("x"), providerallowance.FixtureParseLimits.MaxBytes)...)
	res = runHelper(t, home, file, over, nil, nil)
	if res.code == 0 || res.stderr != captureMsg || strings.Contains(res.stdout, canary) {
		t.Fatal("oversize")
	}
	assertNoLeak(t, res.stdout, res.stderr)
	assertSame(t, file, prior)
	deep := "{}"
	for i := 0; i < 32; i++ {
		deep = `{"k":` + deep + `}`
	}
	if res = runHelper(t, home, file, []byte(deep), nil, nil); res.code == 0 {
		t.Fatal("depth 33")
	}
	assertSame(t, file, prior)
	okDepth := "{}"
	for i := 0; i < 31; i++ {
		okDepth = `{"k":` + okDepth + `}`
	}
	if res = runHelper(t, home, file, []byte(okDepth), nil, nil); res.code != 0 {
		t.Fatal("depth 32")
	}
	prior = writePrior(t, file)
	var many strings.Builder
	many.WriteByte('{')
	for i := 0; i < 65; i++ {
		if i > 0 {
			many.WriteByte(',')
		}
		fmt.Fprintf(&many, `"k%d":1`, i)
	}
	many.WriteByte('}')
	if res = runHelper(t, home, file, []byte(many.String()), nil, nil); res.code == 0 {
		t.Fatal("65 keys")
	}
	assertSame(t, file, prior)
	many.Reset()
	many.WriteByte('{')
	for i := 0; i < 64; i++ {
		if i > 0 {
			many.WriteByte(',')
		}
		fmt.Fprintf(&many, `"k%d":1`, i)
	}
	many.WriteByte('}')
	if res = runHelper(t, home, file, []byte(many.String()), nil, nil); res.code != 0 {
		t.Fatal("64 keys")
	}
	prior = writePrior(t, file)
	if res = runHelper(t, home, file, []byte(`{"pad":"`+strings.Repeat("a", 8193)+`"}`), nil, nil); res.code == 0 {
		t.Fatal("long string")
	}
	assertSame(t, file, prior)
	if res = runHelper(t, home, file, []byte(`{"pad":"`+strings.Repeat("a", 8192)+`"}`), nil, nil); res.code != 0 {
		t.Fatal("string bound")
	}
	assertNoTemps(t, cache)
	res = runHelper(t, home, "read.json", []byte(`{"rate_limits":{}}`), nil, nil)
	if res.code == 0 || res.stderr != captureMsg {
		t.Fatal("relative output")
	}
	if _, err := os.Stat(filepath.Join(home, "read.json")); !os.IsNotExist(err) {
		t.Fatal("relative file created")
	}
}

func TestClaudeStatuslineDestination(t *testing.T) {
	home, cache, file := privatePaths(t)
	prior := writePrior(t, file)
	link := filepath.Join(cache, "link.json")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if res := runHelper(t, home, link, positiveBody(), nil, nil); res.code == 0 {
		t.Fatal("symlink file")
	}
	assertSame(t, file, prior)
	linked := filepath.Join(home, "linked")
	if err := os.Symlink(cache, linked); err != nil {
		t.Fatal(err)
	}
	if res := runHelper(t, home, filepath.Join(linked, "read.json"), positiveBody(), nil, nil); res.code == 0 {
		t.Fatal("symlink parent")
	}
	assertSame(t, file, prior)
	if err := os.Chmod(file, 0o400); err != nil {
		t.Fatal(err)
	}
	if res := runHelper(t, home, file, positiveBody(), nil, nil); res.code == 0 {
		t.Fatal("readonly")
	}
	assertSame(t, file, prior)
	if err := os.Chmod(file, 0o666); err != nil {
		t.Fatal(err)
	}
	if res := runHelper(t, home, file, positiveBody(), nil, nil); res.code == 0 {
		t.Fatal("unsafe mode")
	}
	assertSame(t, file, prior)
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cache, 0o777); err != nil {
		t.Fatal(err)
	}
	if res := runHelper(t, home, file, positiveBody(), nil, nil); res.code == 0 {
		t.Fatal("unsafe directory")
	}
	assertSame(t, file, prior)
	if err := os.Chmod(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	dirDest := filepath.Join(cache, "dir.json")
	if err := os.Mkdir(dirDest, 0o700); err != nil {
		t.Fatal(err)
	}
	if res := runHelper(t, home, dirDest, positiveBody(), nil, nil); res.code == 0 {
		t.Fatal("directory dest")
	}
	assertSame(t, file, prior)
	if res := runHelper(t, home, file, []byte("{"), nil, nil); res.code == 0 {
		t.Fatal("refusal replaced cache")
	}
	assertSame(t, file, prior)
	assertNoTemps(t, cache)
}

func TestClaudeStatuslineDisplay(t *testing.T) {
	home, cache, file := privatePaths(t)
	prior := writePrior(t, file)
	sink := filepath.Join(home, "sink")
	display := filepath.Join(home, "display.sh")
	script := "#!/bin/sh\ncat > \"$DISPLAY_SINK\"\nprintf 'DISPLAY-OK\\n'\n"
	if err := os.WriteFile(display, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	res := runHelper(t, home, file, positiveBody(), []string{"--", display}, []string{"DISPLAY_SINK=" + sink})
	if res.code != 0 || res.stdout != "DISPLAY-OK\n" || res.stderr != "" {
		t.Fatalf("display success %d %q", res.code, res.stderr)
	}
	got, err := os.ReadFile(sink)
	if err != nil || !bytes.Contains(got, []byte(canary)) {
		t.Fatal("display missed stdin")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	assertNoLeak(t, res.stdout, res.stderr, string(raw))
	if err = os.WriteFile(file, prior, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sink); err != nil {
		t.Fatal(err)
	}
	res = runHelper(t, home, file, []byte(`{"credential":"SECRET-CANARY-CRED"`), []string{display}, []string{"DISPLAY_SINK=" + sink})
	if res.code != 0 || res.stdout != "DISPLAY-OK\n" || res.stderr != captureMsg {
		t.Fatal("display on refusal")
	}
	assertNoLeak(t, res.stdout, res.stderr)
	assertSame(t, file, prior)
	sunk, err := os.ReadFile(sink)
	if err != nil || !bytes.Contains(sunk, []byte(canary)) {
		t.Fatal("display lost stdin on refusal")
	}
	failing := filepath.Join(home, "fail.sh")
	if err = os.WriteFile(failing, []byte("#!/bin/sh\ncat >/dev/null\nprintf 'DISPLAY-KEEP\\n'\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	res = runHelper(t, home, file, positiveBody(), []string{failing}, nil)
	if res.code != 3 || res.stdout != "DISPLAY-KEEP\n" {
		t.Fatal("display status")
	}
	if !bytes.Contains(mustRead(t, file), []byte("providerallowance.read.v1")) {
		t.Fatal("capture lost on display failure")
	}
	res = runHelper(t, home, file, positiveBody(), []string{filepath.Join(home, "missing-display")}, nil)
	if res.code != 127 || strings.Contains(res.stderr, canary) {
		t.Fatal("missing display")
	}
	over := append([]byte(canary), bytes.Repeat([]byte("x"), providerallowance.FixtureParseLimits.MaxBytes)...)
	if err := os.Remove(sink); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, prior, 0o600); err != nil {
		t.Fatal(err)
	}
	res = runHelper(t, home, file, over, []string{display}, []string{"DISPLAY_SINK=" + sink})
	if res.code != 0 || res.stdout != "DISPLAY-OK\n" || res.stderr != captureMsg {
		t.Fatal("oversize display")
	}
	assertNoLeak(t, res.stdout, res.stderr)
	assertSame(t, file, prior)
	if sunk, err = os.ReadFile(sink); err != nil || !bytes.Equal(sunk, over) {
		t.Fatal("display lost oversized input")
	}
	assertNoTemps(t, cache)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestClaudeStatuslineNonASCIIDigits(t *testing.T) {
	home, _, file := privatePaths(t)
	prior := writePrior(t, file)
	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked")
	stub := filepath.Join(dir, "consumer-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf x >> '"+marker+"'\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	src := mustRead(t, filepath.Join(toolsDir(t), "claude-statusline-usage.py"))
	script := filepath.Join(dir, "claude-statusline-usage.py")
	if err := os.WriteFile(script, bytes.ReplaceAll(src, []byte(consumerLiteral), []byte(stub)), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"{\"ignored\":\u0661}", "{\"nested\":{\"n\":1.\u0662}}", "{\"ignored\":1e\u0662}", "{\"rate_limits\":{\"five_hour\":{\"used_percentage\":\u0661}}}"} {
		cmd := exec.Command("python3", script, "--output", file)
		cmd.Dir = home
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C", "PYTHONDONTWRITEBYTECODE=1"}
		cmd.Stdin = strings.NewReader(body)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() == 0 || stdout.Len() != 0 || stderr.String() != captureMsg {
			t.Fatal("non-ascii digit accepted")
		}
		assertSame(t, file, prior)
		if _, err = os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("consumer invoked")
		}
	}
}

func TestClaudeStatuslineIncompleteDisplay(t *testing.T) {
	home, _, file := privatePaths(t)
	prior := writePrior(t, file)
	dir := t.TempDir()
	marker := filepath.Join(dir, "invoked")
	stub := filepath.Join(dir, "consumer-stub")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf x >> '"+marker+"'\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "claude-statusline-usage.py")
	if err := os.WriteFile(script, bytes.ReplaceAll(mustRead(t, filepath.Join(toolsDir(t), "claude-statusline-usage.py")), []byte(consumerLiteral), []byte(stub)), 0o755); err != nil {
		t.Fatal(err)
	}
	driver := filepath.Join(dir, "drive.py")
	if err := os.WriteFile(driver, []byte(incompleteDriver), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"early", "readerr"} {
		reads := filepath.Join(dir, mode+"-reads")
		if err := os.Remove(reads); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		cmd := exec.Command("python3", driver, script, file, mode, reads)
		cmd.Dir = home
		cmd.Env = []string{"HOME=" + home, "TMPDIR=" + home, "PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "PYTHONNOUSERSITE=1", "PYTHONDONTWRITEBYTECODE=1"}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err != nil {
			t.Fatalf("%s: %v %q", mode, err, stderr.String())
		}
		if stderr.String() != captureMsg {
			t.Fatalf("%s diagnostic %q", mode, stderr.String())
		}
		assertSame(t, file, prior)
		if _, err = os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("%s consumer invoked", mode)
		}
		gotReads := strings.TrimSpace(string(mustRead(t, reads)))
		switch mode {
		case "early":
			if stdout.String() != "DISPLAY-EARLY\n" || gotReads != "1" {
				t.Fatalf("early display %q reads %s", stdout.String(), gotReads)
			}
		case "readerr":
			if stdout.String() != "DISPLAY-BYTES 34\n" || gotReads != "2" {
				t.Fatalf("read error display %q reads %s", stdout.String(), gotReads)
			}
		}
	}
}

const incompleteDriver = `
import importlib.util
import fcntl
import sys

script, output, mode, reads_path = sys.argv[1:5]
spec = importlib.util.spec_from_file_location("statusline_helper", script)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)

class Chunks:
    def __init__(self, parts, fail_read):
        self.parts = list(parts)
        self.fail_read = fail_read
        self.reads = 0
        self.buffer = self
    def read(self, n):
        self.reads += 1
        if self.fail_read and self.reads > 1:
            raise OSError(5, "injected read")
        if not self.parts:
            return b""
        return self.parts.pop(0)

if mode == "early":
    real_popen = mod.subprocess.Popen
    def popen(args, stdin=None, **kwargs):
        proc = real_popen(args, stdin=stdin, **kwargs)
        if proc.stdin is not None:
            fcntl.fcntl(proc.stdin.fileno(), fcntl.F_SETPIPE_SZ, 4096)
        return proc
    mod.subprocess.Popen = popen
    prefix = b"{}" + b" " * (65536 - 2)
    stream = Chunks([prefix, b"MALFORMED", b"Z" * (mod.MAX_BYTES + 1)], False)
    display = [sys.executable, "-c", "import os,sys; os.close(0); sys.stdout.write('DISPLAY-EARLY\\n'); sys.stdout.flush(); raise SystemExit(0)"]
elif mode == "readerr":
    prefix = b"{}" + b" " * 32
    stream = Chunks([prefix], True)
    display = [sys.executable, "-c", "import sys; data=sys.stdin.buffer.read(); sys.stdout.write('DISPLAY-BYTES %d\\n' % len(data)); raise SystemExit(0)"]
else:
    raise SystemExit(2)

mod.sys.stdin = stream
code = mod.main(["claude-statusline-usage", "--output", output, "--display", "--", *display])
with open(reads_path, "w", encoding="ascii") as handle:
    handle.write("%d\n" % stream.reads)
raise SystemExit(code)
`

func TestClaudeStatuslineCaptureOverflowStops(t *testing.T) {
	home, _, file := privatePaths(t)
	prior := writePrior(t, file)
	statuslinePair(t)
	cmd := exec.Command("python3", helperScript, "--output", file)
	cmd.Env = []string{"HOME=" + home, "TMPDIR=" + home, "PATH=/usr/bin:/bin", "LANG=C", "PYTHONDONTWRITEBYTECODE=1"}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = reader
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write(bytes.Repeat([]byte("x"), providerallowance.FixtureParseLimits.MaxBytes+1)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() == 0 || stderr.String() != captureMsg {
			t.Fatal("overflow accepted")
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("capture overflow waited for EOF")
	}
	_ = writer.Close()
	assertSame(t, file, prior)
}

func TestClaudeStatuslineImageOptIn(t *testing.T) {
	dir := toolsDir(t)
	src := mustRead(t, filepath.Join(dir, "claude-statusline-usage.py"))
	docker := mustRead(t, filepath.Join(dir, "..", "Dockerfile"))
	if !bytes.Contains(docker, []byte("COPY images/agent/tools/claude-statusline-usage.py /usr/local/bin/claude-statusline-usage\n")) {
		t.Fatal("copy")
	}
	if !bytes.Contains(docker, []byte("RUN chmod 0755 /usr/local/bin/claude-statusline-usage\n")) {
		t.Fatal("chmod")
	}
	if bytes.Contains(docker, []byte("statusLine")) || bytes.Contains(src, []byte("statusLine")) || bytes.Contains(src, []byte("settings.json")) {
		t.Fatal("activation")
	}
	if !bytes.Contains(src, []byte("/usr/local/bin/agent-run")) || !bytes.Contains(src, []byte("--claude-statusline")) || bytes.Contains(src, []byte("--from-file")) {
		t.Fatal("consumer wiring")
	}
	for _, banned := range []string{"urllib", "socket", "requests"} {
		if bytes.Contains(src, []byte(banned)) {
			t.Fatal("network import")
		}
	}
}

func TestClaudeStatuslineUsageArgs(t *testing.T) {
	home, _, _ := privatePaths(t)
	statuslinePair(t)
	cmd := exec.Command("python3", helperScript)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "LANG=C"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || stderr.String() != usageMsg {
		t.Fatal("usage")
	}
}
