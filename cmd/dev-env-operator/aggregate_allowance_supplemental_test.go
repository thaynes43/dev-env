package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const supplementalSentinel = "SENTINEL-NATIVE-READER-7f3a9c"

func approvedSupplementalFlag() string {
	return `{"namespace":"dev-agents","name":"dev-env-1104-010332","sessionUID":"9323443a-4a8f-404f-8b41-b583e3bcc1a9","serviceAccount":"dev-env-workbench","serviceAccountUID":"55cc1f98-5b17-46e7-8770-c8bf9de85437"}`
}

func TestSupplementalReaderDefaultAbsent(t *testing.T) {
	bare, err := parseFlags(nil)
	if err != nil || bare.aggregateAllowance != nil {
		t.Fatalf("default %#v %v", bare.aggregateAllowance, err)
	}
	valid, err := parseFlags([]string{"--enable-aggregate-allowance-reports", "--aggregate-allowance-config=" + aggregateAllowanceJSON(t)})
	if err != nil || valid.aggregateAllowance == nil || valid.aggregateAllowance.supplemental != nil {
		t.Fatalf("private selection gained a public reader %#v %v", valid.aggregateAllowance, err)
	}
	reports, err := aggregateAllowanceReports(valid.aggregateAllowance, nil, nil)
	if err == nil || reports != nil {
		t.Fatal("nil clients built a handler", err)
	}
}

func TestSupplementalReaderBindingAndCaps(t *testing.T) {
	seven := aggregateAllowanceReaders(t, []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7"})
	got, err := parseFlags([]string{
		"--enable-aggregate-allowance-reports",
		"--aggregate-allowance-config=" + seven,
		"--aggregate-allowance-supplemental-reader=" + approvedSupplementalFlag(),
	})
	if err != nil || got.aggregateAllowance == nil || got.aggregateAllowance.supplemental == nil {
		t.Fatal(err)
	}
	sup := got.aggregateAllowance.supplemental
	if sup.Namespace != "dev-agents" || sup.Name != "dev-env-1104-010332" || sup.SessionUID != "9323443a-4a8f-404f-8b41-b583e3bcc1a9" || sup.ServiceAccount != "dev-env-workbench" || sup.ServiceAccountUID != "55cc1f98-5b17-46e7-8770-c8bf9de85437" {
		t.Fatalf("binding %#v", sup)
	}
	if len(got.aggregateAllowance.readerUIDs) != 7 || got.aggregateAllowance.readerSA != "dev-env-agent" {
		t.Fatalf("private selection changed %#v", got.aggregateAllowance)
	}
	for _, uid := range got.aggregateAllowance.readerUIDs {
		if uid == sup.SessionUID {
			t.Fatal("public UID entered the private list")
		}
	}
	cl := fake.NewClientBuilder().Build()
	reports, err := aggregateAllowanceReports(got.aggregateAllowance, cl, cl)
	if err != nil || reports == nil || !reports.RouteEnabled() || !reports.PublishEnabled() {
		t.Fatal(err)
	}
	if reports.Supplemental == nil || reports.Supplemental.SessionUID != sup.SessionUID || len(reports.SessionUIDs) != 7 || reports.SessionServiceAccount != "dev-env-agent" {
		t.Fatalf("handler binding %#v uids %v", reports.Supplemental, reports.SessionUIDs)
	}
	for _, uid := range reports.SessionUIDs {
		if uid == reports.Supplemental.SessionUID {
			t.Fatal("handler appended the public UID")
		}
	}
	if reports.PortalNamespace != aggregatePortalNamespace || reports.PortalServiceAccount != aggregatePortalAccount || reports.PublisherSessionUID != "publisher-uid" {
		t.Fatal("portal or publisher changed")
	}
}

func TestSupplementalReaderExplicitEmptyRefused(t *testing.T) {
	for _, flagArgs := range [][]string{
		{"--aggregate-allowance-supplemental-reader="},
		{"--aggregate-allowance-supplemental-reader", ""},
	} {
		args := append([]string{
			"--enable-aggregate-allowance-reports",
			"--aggregate-allowance-config=" + aggregateAllowanceJSON(t),
		}, flagArgs...)
		stderr, err := parseFlagsStderr(t, args)
		if !errors.Is(err, errSupplementalAllowanceReader) || err.Error() != errSupplementalAllowanceReader.Error() || stderr != "" {
			t.Fatalf("explicit empty argument: error %q stderr %q", err, stderr)
		}
	}
}

func TestSupplementalReaderRefusesOverlapAndNinth(t *testing.T) {
	overlap := aggregateAllowanceReaders(t, []string{"r1", "9323443a-4a8f-404f-8b41-b583e3bcc1a9"})
	if _, err := parseFlags([]string{"--enable-aggregate-allowance-reports", "--aggregate-allowance-config=" + overlap, "--aggregate-allowance-supplemental-reader=" + approvedSupplementalFlag()}); !errors.Is(err, errSupplementalAllowanceReader) || strings.Contains(err.Error(), "9323443a") {
		t.Fatal(err)
	}
	eight := aggregateAllowanceReaders(t, []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8"})
	if _, err := parseFlags([]string{"--enable-aggregate-allowance-reports", "--aggregate-allowance-config=" + eight, "--aggregate-allowance-supplemental-reader=" + approvedSupplementalFlag()}); !errors.Is(err, errSupplementalAllowanceReader) {
		t.Fatal(err)
	}
}

func TestSupplementalReaderInvalidInputDoesNotEcho(t *testing.T) {
	base := approvedSupplementalFlag()
	cases := []string{
		strings.TrimRight(base, "}") + `,"leak":"` + supplementalSentinel + `"}`,
		strings.Replace(base, `"serviceAccountUID":"55cc1f98-5b17-46e7-8770-c8bf9de85437"`, `"serviceAccountUID":null`, 1),
		strings.Replace(base, `"sessionUID"`, `"sessionUid"`, 1),
		strings.Replace(base, `"namespace":"dev-agents"`, `"namespace":"dev-agents","namespace":"dev-agents"`, 1),
		strings.Replace(base, `"serviceAccount":"dev-env-workbench"`, `"serviceAccount":"dev-agents/dev-env-workbench"`, 1),
		strings.Replace(base, `"namespace":"dev-agents"`, `"namespace":"other"`, 1),
		`{"namespace":"dev-agents","name":"dev-env-1104-010332","sessionUID":"` + supplementalSentinel + `"}`,
		supplementalSentinel,
		"\xef\xbb\xbf" + base,
		`null`,
		base + supplementalSentinel,
	}
	for _, raw := range cases {
		stderr, err := parseFlagsStderr(t, []string{
			"--enable-aggregate-allowance-reports",
			"--aggregate-allowance-config=" + aggregateAllowanceJSON(t),
			"--aggregate-allowance-supplemental-reader=" + raw,
		})
		if !errors.Is(err, errSupplementalAllowanceReader) || err.Error() != errSupplementalAllowanceReader.Error() || strings.Contains(err.Error(), supplementalSentinel) || strings.Contains(stderr, supplementalSentinel) || strings.Contains(stderr, raw) {
			t.Fatalf("echoed error %q stderr %q", err, stderr)
		}
	}
	stderr, err := parseFlagsStderr(t, []string{
		"--enable-aggregate-allowance-reports",
		"--aggregate-allowance-config=" + aggregateAllowanceJSON(t),
		"--aggregate-allowance-supplemental-reader=" + base,
		"--aggregate-allowance-supplemental-reader=" + strings.Replace(base, `"name":"dev-env-1104-010332"`, `"name":"`+supplementalSentinel+`"`, 1),
	})
	if !errors.Is(err, errSupplementalAllowanceReader) || strings.Contains(err.Error(), supplementalSentinel) || strings.Contains(stderr, supplementalSentinel) {
		t.Fatalf("repeated argument echoed %q stderr %q", err, stderr)
	}
}

func TestSupplementalReaderRefusesBeforeRunnables(t *testing.T) {
	raw := strings.TrimRight(approvedSupplementalFlag(), "}") + `,"leak":"` + supplementalSentinel + `"}`
	args := []string{
		"--enable-aggregate-allowance-reports",
		"--aggregate-allowance-config=" + aggregateAllowanceJSON(t),
		"--aggregate-allowance-supplemental-reader=" + raw,
	}
	var err error
	stderr := captureStderr(t, func() {
		err = run(args)
	})
	if !errors.Is(err, errSupplementalAllowanceReader) || strings.Contains(err.Error(), supplementalSentinel) || strings.Contains(stderr, supplementalSentinel) || strings.Contains(stderr, "starting") {
		t.Fatalf("startup %q stderr %q", err, stderr)
	}
	stderr, err = parseFlagsStderr(t, []string{"--aggregate-allowance-supplemental-reader=" + approvedSupplementalFlag()})
	if !errors.Is(err, errSupplementalAllowanceReader) || strings.Contains(stderr, "dev-env-workbench") {
		t.Fatalf("missing private selection %q stderr %q", err, stderr)
	}
	private := strings.Replace(aggregateAllowanceJSON(t), `"registry"`, `"supplemental":"`+supplementalSentinel+`","registry"`, 1)
	if _, err = parseFlags([]string{"--enable-aggregate-allowance-reports", "--aggregate-allowance-config=" + private}); !errors.Is(err, errAggregateAllowanceConfig) || strings.Contains(err.Error(), supplementalSentinel) {
		t.Fatal("private schema accepted a public field", err)
	}
}

func TestSupplementalReaderPrivateFileSelection(t *testing.T) {
	seven := aggregateAllowanceReaders(t, []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7"})
	path := writeAggregateAllowanceFile(t, seven)
	got, err := parseFlags([]string{
		"--enable-aggregate-allowance-reports",
		"--aggregate-allowance-config-file=" + path,
		"--aggregate-allowance-supplemental-reader=" + approvedSupplementalFlag(),
	})
	if err != nil || got.aggregateAllowance == nil || got.aggregateAllowance.supplemental == nil || len(got.aggregateAllowance.readerUIDs) != 7 {
		t.Fatal(err)
	}
	cl := fake.NewClientBuilder().Build()
	reports, err := aggregateAllowanceReports(got.aggregateAllowance, cl, cl)
	if err != nil || reports == nil || reports.Supplemental == nil || reports.Supplemental.SessionUID != got.aggregateAllowance.supplemental.SessionUID || len(reports.SessionUIDs) != 7 {
		t.Fatal(err)
	}
	inline, err := parseFlags([]string{
		"--enable-aggregate-allowance-reports",
		"--aggregate-allowance-config=" + seven,
		"--aggregate-allowance-supplemental-reader=" + approvedSupplementalFlag(),
	})
	if err != nil || inline.aggregateAllowance == nil || inline.aggregateAllowance.supplemental == nil || len(inline.aggregateAllowance.readerUIDs) != 7 {
		t.Fatal("inline selection", err)
	}
	eight := writeAggregateAllowanceFile(t, aggregateAllowanceReaders(t, []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8"}))
	if _, err = parseFlags([]string{"--enable-aggregate-allowance-reports", "--aggregate-allowance-config-file=" + eight, "--aggregate-allowance-supplemental-reader=" + approvedSupplementalFlag()}); !errors.Is(err, errSupplementalAllowanceReader) {
		t.Fatal(err)
	}
	overlap := writeAggregateAllowanceFile(t, aggregateAllowanceReaders(t, []string{"r1", "9323443a-4a8f-404f-8b41-b583e3bcc1a9"}))
	stderr, err := parseFlagsStderr(t, []string{"--enable-aggregate-allowance-reports", "--aggregate-allowance-config-file=" + overlap, "--aggregate-allowance-supplemental-reader=" + approvedSupplementalFlag()})
	if !errors.Is(err, errSupplementalAllowanceReader) || strings.Contains(err.Error(), "9323443a") || strings.Contains(stderr, "9323443a") || strings.Contains(stderr, supplementalSentinel) {
		t.Fatalf("file overlap %q stderr %q", err, stderr)
	}
	missing := filepath.Join(t.TempDir(), "missing.json")
	if _, err = parseFlags([]string{"--enable-aggregate-allowance-reports", "--aggregate-allowance-config-file=" + missing, "--aggregate-allowance-supplemental-reader=" + approvedSupplementalFlag()}); !errors.Is(err, errAggregateAllowanceConfig) || strings.Contains(err.Error(), "dev-env-workbench") {
		t.Fatal(err)
	}
}

func writeAggregateAllowanceFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "selection.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func aggregateAllowanceReaders(t *testing.T, uids []string) string {
	t.Helper()
	quoted := make([]string, len(uids))
	for i, uid := range uids {
		quoted[i] = `"` + uid + `"`
	}
	list := `"readerSessionUIDs":[` + strings.Join(quoted, ",") + `]`
	base := aggregateAllowanceJSON(t)
	old := `"readerSessionUIDs":["reader-uid"]`
	if !strings.Contains(base, old) {
		t.Fatalf("fixture reader list changed")
	}
	return strings.Replace(base, old, list, 1)
}

func parseFlagsStderr(t *testing.T, args []string) (string, error) {
	t.Helper()
	var err error
	stderr := captureStderr(t, func() {
		_, err = parseFlags(args)
	})
	return stderr, err
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = write
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, read)
		done <- buf.String()
	}()
	defer func() {
		if os.Stderr == write {
			os.Stderr = old
			_ = write.Close()
		}
	}()
	fn()
	os.Stderr = old
	_ = write.Close()
	return <-done
}
