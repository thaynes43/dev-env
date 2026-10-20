package keeper

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// createResponse is PVE's real `pvesh create` shape (seen live 2026-10-20):
// info.expire and info.privsep are JSON strings.
func createResponse(e credentialEntry, expire, privsep string) []byte {
	return []byte(`{"full-tokenid":"` + providerID(e.Spec.Grant.UID) + `","info":{"comment":"` + providerComment(e) + `","expire":` + expire + `,"privsep":` + privsep + `},"value":"` + credentialCanary + `"}`)
}

func TestProxmoxMintAcceptsRealCreateResponseShape(t *testing.T) {
	f := newCredentialFixture(t)
	e := credentialEntry{JobUID: testJobUID, Spec: f.job.Spec, Stage: journalIntent}
	unix := e.Spec.ExpiresAt.Unix()
	quoted := `"` + itoa(unix) + `"`
	for name, tc := range map[string]struct {
		expire, privsep string
		want            bool
	}{
		"real create shape, strings":   {quoted, `"0"`, true},
		"integers":                     {itoa(unix), `0`, true},
		"mixed string expire int priv": {quoted, `0`, true},
		"privsep bool false":           {quoted, `false`, true},
		"privsep string one":           {quoted, `"1"`, false},
		"privsep int one":              {quoted, `1`, false},
		"privsep true":                 {quoted, `true`, false},
		"privsep empty string":         {quoted, `""`, false},
		"privsep text":                 {quoted, `"false"`, false},
		"expire wrong value":           {`"` + itoa(unix+1) + `"`, `"0"`, false},
		"expire non-numeric":           {`"soon"`, `"0"`, false},
		"expire empty":                 {`""`, `"0"`, false},
		"expire padded":                {`" ` + itoa(unix) + `"`, `"0"`, false},
		"expire plus sign":             {`"+` + itoa(unix) + `"`, `"0"`, false},
		"expire fraction":              {itoa(unix) + `.5`, `"0"`, false},
		"expire bool":                  {`true`, `"0"`, false},
		"expire null":                  {`null`, `"0"`, false},
	} {
		t.Run(name, func(t *testing.T) {
			p := &proxmoxProvider{SSH: &providerResponse{raw: createResponse(e, tc.expire, tc.privsep)}}
			result, err := p.mint(context.Background(), e)
			if (err == nil) != tc.want || !result.PossibleDispatch {
				t.Fatalf("got err=%v want accepted=%v", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), credentialCanary) {
				t.Fatal("refusal leaked the token value")
			}
			if tc.want && (result.TokenID != providerID(testGrantUID) || result.TokenSecret.Reveal() != credentialCanary) {
				t.Fatal("accepted mint lost its identity or secret")
			}
		})
	}
}

// Wider number forms must not loosen the identity and secret checks.
func TestProxmoxMintStillChecksIdentityCommentAndSecret(t *testing.T) {
	f := newCredentialFixture(t)
	e := credentialEntry{JobUID: testJobUID, Spec: f.job.Spec, Stage: journalIntent}
	good := string(createResponse(e, `"`+itoa(e.Spec.ExpiresAt.Unix())+`"`, `"0"`))
	for name, bad := range map[string]string{
		"other token id": strings.Replace(good, providerID(testGrantUID), "dev-env@pve!grant-other", 1),
		"other comment":  strings.Replace(good, providerComment(e), "dev-env:x:y", 1),
		"short secret":   strings.Replace(good, credentialCanary, "short", 1),
		"quoted secret":  strings.Replace(good, credentialCanary, `a\"b-0123456789abcdef`, 1),
		"two documents":  good + good,
		"empty":          "",
	} {
		p := &proxmoxProvider{SSH: &providerResponse{raw: []byte(bad)}}
		if _, err := p.mint(context.Background(), e); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	p := &proxmoxProvider{SSH: &providerResponse{raw: []byte(good)}}
	if _, err := p.mint(context.Background(), e); err != nil {
		t.Fatalf("control response refused: %v", err)
	}
}

// The list call returns integers (and may return strings); both prove the same
// ownership, and a malformed or different value is an ownership mismatch.
func TestProxmoxInspectAcceptsListShapeAndStringForms(t *testing.T) {
	f := newCredentialFixture(t)
	e := credentialEntry{JobUID: testJobUID, Spec: f.job.Spec, Stage: journalCleanup}
	unix := itoa(e.Spec.ExpiresAt.Unix())
	row := func(expire, privsep string) []byte {
		return []byte(`[{"tokenid":"` + providerName(testGrantUID) + `","expire":` + expire + `,"privsep":` + privsep + `,"comment":"` + providerComment(e) + `"}]`)
	}
	for name, tc := range map[string]struct {
		expire, privsep string
		present         bool
		wantErr         bool
	}{
		"list shape, integers": {unix, `0`, true, false},
		"strings":              {`"` + unix + `"`, `"0"`, true, false},
		"privsep false":        {unix, `false`, true, false},
		"other expiry":         {`1`, `0`, true, true},
		"malformed expiry":     {`"x"`, `0`, true, true},
		"privsep enabled":      {unix, `1`, true, true},
		"privsep malformed":    {unix, `"zero"`, true, true},
	} {
		t.Run(name, func(t *testing.T) {
			p := &proxmoxProvider{SSH: &providerResponse{raw: row(tc.expire, tc.privsep)}}
			present, err := p.inspect(context.Background(), e)
			if present != tc.present || (err != nil) != tc.wantErr {
				t.Fatalf("present=%v err=%v", present, err)
			}
		})
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// A mint that ends AmbiguousMint logs one fixed line: a reason class and the
// job name, never token material or command output.
func TestCredentialAmbiguousMintLogsReasonOnly(t *testing.T) {
	f := newCredentialFixture(t)
	f.provider.loseResponse = true
	var lines []string
	f.w.Log = funcr.New(func(prefix, args string) { lines = append(lines, prefix+args) }, funcr.Options{}).WithName("test")
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if f.status(t).Phase != v1alpha1.CredentialCleanupPending {
		t.Fatal("job did not go to cleanup")
	}
	var found int
	for _, line := range lines {
		if strings.Contains(line, "credential mint ended ambiguous") {
			found++
			if !strings.Contains(line, "proxmox mint command failed") || strings.Contains(line, credentialCanary) {
				t.Fatalf("log line has the wrong content: %s", line)
			}
		}
	}
	if found != 1 {
		t.Fatalf("want exactly one ambiguous-mint line, got %d in %v", found, lines)
	}
}
