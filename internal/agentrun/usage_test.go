package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

type failReader struct{}

func (failReader) Read([]byte) (int, error) { panic("unexpected stdin") }

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("private") }
func TestUsageLocalPreflight(t *testing.T) {
	var out, errout bytes.Buffer
	at := time.Date(2026, 10, 28, 20, 0, 0, 0, time.UTC)
	env := Env{Stdout: &out, Stderr: &errout, Stdin: failReader{}, Now: func() time.Time { return at }}
	if Run(context.Background(), []string{"usage", "-o", "json"}, env) != 0 {
		t.Fatal(errout.String())
	}
	read, err := providerallowance.DecodeReadResponse(out.Bytes(), providerallowance.FixtureParseLimits)
	if err != nil || len(read.Items) != 0 || len(read.SourceStatuses) != 3 || read.ServedAt != at.Format(time.RFC3339) {
		t.Fatal("invalid local read", err)
	}
	for _, s := range read.SourceStatuses {
		if s.Availability != providerallowance.AvailabilityUnconfigured || s.LastGood != nil || s.LastAttempt.Result != providerallowance.AttemptUnavailable || s.LastAttempt.AttemptedAt != read.ServedAt || s.Freshness.State != providerallowance.FreshUnknown || s.Binding.State != providerallowance.BindingUnknown || s.LastAttempt.Code == nil || !strings.Contains(*s.LastAttempt.Code, "no_provider_request") {
			t.Fatal("invalid preflight", s)
		}
	}
}
func TestUsageCacheAndRefusals(t *testing.T) {
	fixture, err := os.ReadFile("../providerallowance/testdata/read-interface-examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var wrapped struct {
		HTTP struct {
			Body json.RawMessage `json:"body"`
		} `json:"http"`
	}
	if json.Unmarshal(fixture, &wrapped) != nil {
		t.Fatal("fixture")
	}
	original, err := providerallowance.DecodeReadResponse(wrapped.HTTP.Body, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	var fixtureOut bytes.Buffer
	fixtureEnv := Env{Stdout: &fixtureOut, Stderr: io.Discard, Stdin: bytes.NewReader(wrapped.HTTP.Body)}
	if Run(context.Background(), []string{"usage", "-o", "json", "--from-file", "-"}, fixtureEnv) != 0 {
		t.Fatal("fixture refused")
	}
	restored, err := providerallowance.DecodeReadResponse(fixtureOut.Bytes(), providerallowance.FixtureParseLimits)
	if err != nil || !reflect.DeepEqual(original, restored) {
		t.Fatal("fixture changed", err)
	}
	var good bytes.Buffer
	env := Env{Stdout: &good, Stderr: io.Discard, Now: func() time.Time { return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) }}
	if Run(context.Background(), []string{"usage", "-o", "json"}, env) != 0 {
		t.Fatal("default")
	}
	raw := append([]byte(nil), good.Bytes()...)
	file := filepath.Join(t.TempDir(), "cache")
	if os.WriteFile(file, raw, 0600) != nil {
		t.Fatal("write")
	}
	var out bytes.Buffer
	env.Stdout = &out
	env.Now = time.Now
	if Run(context.Background(), []string{"usage", "-o", "json", "--from-file", file}, env) != 0 || !bytes.Equal(out.Bytes(), raw) {
		t.Fatal("cached read changed")
	}
	link := file + "-link"
	if os.Symlink(file, link) != nil {
		t.Fatal("link")
	}
	for _, input := range []string{link, file + "-missing", filepath.Dir(file)} {
		out.Reset()
		var stderr bytes.Buffer
		env.Stderr = &stderr
		if Run(context.Background(), []string{"usage", "-o", "json", "--from-file", input}, env) == 0 || out.Len() != 0 || strings.Contains(stderr.String(), input) {
			t.Fatal("unsafe refusal")
		}
	}
	for _, bad := range []string{strings.Repeat("x", providerallowance.FixtureParseLimits.MaxBytes+1), "{", strings.Replace(string(raw), `"schema":`, `"schema":null,"schema":`, 1), strings.Replace(string(raw), `"schema":`, `"Schema":`, 1), strings.Replace(string(raw), `"schema":`, `"ſchema":`, 1), strings.Replace(string(raw), `"schema":`, `"schema":null,"extra":1,"duplicate":`, 1), strings.Replace(string(raw), `"items":[]`, `"items":null`, 1)} {
		env.Stdin = strings.NewReader(bad)
		out.Reset()
		if Run(context.Background(), []string{"usage", "-o", "json", "--from-file", "-"}, env) == 0 || out.Len() != 0 {
			t.Fatal("bad cache accepted")
		}
	}
	env.Stdin = failReader{}
	if Run(context.Background(), []string{"usage", "-o", "json", "--from-file", "-", "--bad"}, env) == 0 {
		t.Fatal("invalid flag accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Run(ctx, []string{"usage", "-o", "json"}, env) == 0 {
		t.Fatal("cancel accepted")
	}
	env.Stdout = failWriter{}
	if Run(context.Background(), []string{"usage", "-o", "json"}, env) == 0 {
		t.Fatal("write failure accepted")
	}
}
