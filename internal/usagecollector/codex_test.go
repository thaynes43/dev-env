package usagecollector

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestCodexOfficialRPCReadSanitizesAndBracketsIdentity(t *testing.T) {
	for _, variant := range []string{"success", "matching-account-notification", "mid-bracket-matching-notification", "hidden-auth-alias", "account-change", "plan-mismatch", "missing-used", "null-used", "out-of-range", "duplicate-used", "refresh-request", "auth-notification", "wrong-response", "legacy-conflict"} {
		t.Run(variant, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			requests := make(chan []string, 1)
			go func() {
				defer func() { _ = server.Close() }()
				reader := bufio.NewReader(server)
				var methods []string
				for i := 0; i < 6; i++ {
					line, err := reader.ReadBytes('\n')
					if err != nil {
						requests <- methods
						return
					}
					var req struct {
						ID     int             `json:"id"`
						Method string          `json:"method"`
						Params json.RawMessage `json:"params"`
					}
					_ = json.Unmarshal(line, &req)
					methods = append(methods, req.Method)
					if req.Method == "initialized" {
						continue
					}
					var result string
					switch req.Method {
					case "initialize":
						result = `{"userAgent":"fixture"}`
					case "account/read":
						if req.ID == 3 && variant == "matching-account-notification" {
							_, _ = io.WriteString(server, "{\"method\":\"account/updated\",\"params\":{\"authMode\":\"chatgpt\",\"planType\":\"pro\"}}\n")
						}
						if string(req.Params) != `{"refreshToken":false}` {
							requests <- append(methods, "BAD-FORCED-REFRESH")
							return
						}
						email := "fixture@example.invalid"
						if req.ID == 5 && variant == "account-change" {
							email = "different@example.invalid"
						}
						result = `{"account":{"type":"chatgpt","email":"` + email + `","planType":"pro"},"requiresOpenaiAuth":true}`
					case "account/rateLimits/read":
						if variant == "hidden-auth-alias" {
							_, _ = io.WriteString(server, "{\"method\":\"account/updated\",\"Method\":\"other\",\"params\":{\"authMode\":\"chatgpt\",\"planType\":\"pro\"}}\n")
						}
						if variant == "mid-bracket-matching-notification" {
							_, _ = io.WriteString(server, "{\"method\":\"account/updated\",\"params\":{\"authMode\":\"chatgpt\",\"planType\":\"pro\"}}\n")
							requests <- methods
							return
						}
						if variant == "refresh-request" {
							_, _ = io.WriteString(server, "{\"id\":99,\"method\":\"account/chatgptAuthTokens/refresh\",\"params\":{}}\n")
							requests <- methods
							return
						}
						if variant == "auth-notification" {
							_, _ = io.WriteString(server, "{\"method\":\"account/updated\",\"params\":{}}\n")
							requests <- methods
							return
						}
						bucket := `{"limitId":"codex","planType":"pro","primary":{"usedPercent":12,"windowDurationMins":10080,"resetsAt":1794224930},"secondary":null}`
						switch variant {
						case "plan-mismatch":
							bucket = strings.Replace(bucket, `"pro"`, `"team"`, 1)
						case "missing-used":
							bucket = strings.Replace(bucket, `"usedPercent":12,`, "", 1)
						case "null-used":
							bucket = strings.Replace(bucket, `"usedPercent":12`, `"usedPercent":null`, 1)
						case "out-of-range":
							bucket = strings.Replace(bucket, `"usedPercent":12`, `"usedPercent":101`, 1)
						case "duplicate-used":
							bucket = strings.Replace(bucket, `"usedPercent":12`, `"usedPercent":12,"usedPercent":0`, 1)
						}
						legacy := bucket
						if variant == "legacy-conflict" {
							legacy = strings.Replace(bucket, `"usedPercent":12`, `"usedPercent":11`, 1)
						}
						result = `{"accountId":null,"rateLimits":` + legacy + `,"rateLimitsByLimitId":{"codex":` + bucket + `},"rateLimitResetCredits":{"availableCount":999,"credits":[]},"rateLimitUpsell":{"secret":"DISCARDED"}}`
					default:
						requests <- append(methods, "BAD-METHOD")
						return
					}
					responseID := req.ID
					if variant == "wrong-response" {
						responseID = 99
					}
					reply, _ := json.Marshal(map[string]any{"id": responseID, "result": json.RawMessage(result)})
					reply = append(reply, '\n')
					if _, err = server.Write(reply); err != nil {
						requests <- methods
						return
					}
				}
				requests <- methods
			}()
			now := time.Date(2026, 11, 2, 18, 2, 10, 0, time.UTC)
			got, err := collectCodexRPC(client, client, func() time.Time { return now })
			_ = client.Close()
			methods := <-requests
			for _, m := range methods {
				if m != "initialize" && m != "initialized" && m != "account/read" && m != "account/rateLimits/read" {
					t.Fatalf("unexpected method %s", m)
				}
			}
			if variant != "success" && variant != "matching-account-notification" {
				if err == nil {
					t.Fatal("invalid observation accepted")
				}
				return
			}
			if err != nil || got.Primary == nil || got.Primary.UsedPercent != 12 || got.Secondary != nil || got.ProviderObservedAt != nil || got.ReceivedAt != now.Format(time.RFC3339Nano) || got.PlanType != "pro" {
				t.Fatalf("capture %+v %v", got, err)
			}
			if len(methods) != 6 || methods[2] != "account/read" || methods[3] != "account/read" || methods[5] != "account/read" {
				t.Fatalf("account bracket absent %v", methods)
			}
			body, _ := json.Marshal(got)
			for _, s := range []string{"fixture@example", "DISCARDED", "999", "credits"} {
				if bytes.Contains(body, []byte(s)) {
					t.Fatal("unrelated private data retained")
				}
			}
			if bytes.Contains(body, []byte("ordinaryUsageAllowed")) {
				t.Fatal("absent ordinaryUsageAllowed was invented")
			}
		})
	}
}
func TestCodexUnsupportedAccountDoesNotInventWorkspace(t *testing.T) {
	for _, raw := range []string{`{"account":null}`, `{"account":{"type":"apiKey"}}`, `{"account":{"type":"chatgpt","email":null,"planType":"pro"}}`, `{"account":{"type":"chatgpt","email":"fixture@example.invalid","planType":"team"}}`} {
		if _, err := decodeAccount([]byte(raw)); err == nil {
			t.Fatal("unsupported account accepted")
		}
	}
}

func TestCodexPreferredBucketSupportsByIDOnlyAndLegacyOtherBucket(t *testing.T) {
	account := codexAccount{ref: "openai-email-sha256:" + strings.Repeat("a", 64), plan: "pro"}
	codex := `{"limitId":"codex","primary":{"usedPercent":0,"windowDurationMins":10080,"resetsAt":1794224930},"secondary":null}`
	for _, legacy := range []string{"", `"rateLimits":{"limitId":"other-model","primary":{"usedPercent":99}},`} {
		raw := []byte(`{` + legacy + `"rateLimitsByLimitId":{"codex":` + codex + `}}`)
		report, err := sanitizeCodexQuota(account, raw, time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC))
		if err != nil || report.Primary == nil || report.Primary.UsedPercent != 0 {
			t.Fatal("preferred supported bucket refused", err)
		}
	}
}

func TestCodexRPCMetadataRejectsCaseAliases(t *testing.T) {
	if _, err := decodeAccount([]byte(`{"account":{"type":"chatgpt","email":"fixture@example.invalid","Email":"other@example.invalid","planType":"pro"}}`)); err == nil {
		t.Fatal("email alias changed identity")
	}
	account := codexAccount{ref: "openai-email-sha256:" + strings.Repeat("a", 64), plan: "pro"}
	for _, raw := range []string{
		`{"rateLimits":{"limitId":"codex","LimitId":"other","primary":{"usedPercent":12}}}`,
		`{"rateLimits":{"limitId":"codex","planType":"pro","PlanType":"plus","primary":{"usedPercent":12}}}`,
		`{"rateLimits":{"limitId":"codex","primary":{"usedPercent":12,"UsedPercent":99}}}`,
	} {
		if _, err := sanitizeCodexQuota(account, []byte(raw), time.Now()); err == nil {
			t.Fatal("quota metadata case alias accepted")
		}
	}
}

func TestSanitizeCodexOrdinaryUsageAllowed(t *testing.T) {
	account := codexAccount{ref: "openai-email-sha256:" + strings.Repeat("a", 64), plan: "pro"}
	at := time.Date(2026, 11, 2, 18, 2, 10, 0, time.UTC)
	base := `{"limitId":"codex","planType":"pro","primary":{"usedPercent":12,"windowDurationMins":10080,"resetsAt":1794224930},"secondary":null}`
	quota := func(extra string) []byte {
		return []byte(`{"rateLimits":` + base + `,"rateLimitsByLimitId":{"codex":` + base + `}` + extra + `}`)
	}
	must := func(t *testing.T, raw []byte) (CodexAccountCapture, []byte) {
		t.Helper()
		report, err := sanitizeCodexQuota(account, raw, at)
		if err != nil {
			t.Fatal(err)
		}
		if report.ProviderObservedAt != nil || report.Primary == nil || report.Primary.UsedPercent != 12 || report.ReceivedAt != at.UTC().Format(time.RFC3339Nano) {
			t.Fatalf("reading or clock changed %+v", report)
		}
		body, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := providerallowance.DecodeCodexAccountCapture(body)
		if err != nil {
			t.Fatal(err)
		}
		if string(decoded.OrdinaryUsageAllowed) != string(report.OrdinaryUsageAllowed) || decoded.ProviderObservedAt != nil || decoded.Primary == nil || decoded.Primary.UsedPercent != 12 {
			t.Fatalf("decoder changed capture %+v", decoded)
		}
		return report, body
	}
	t.Run("missing", func(t *testing.T) {
		report, body := must(t, quota(""))
		if len(report.OrdinaryUsageAllowed) != 0 || bytes.Contains(body, []byte("ordinaryUsageAllowed")) {
			t.Fatal("missing scalar became a value")
		}
	})
	t.Run("null", func(t *testing.T) {
		report, body := must(t, quota(`,"ordinaryUsageAllowed":null`))
		if string(report.OrdinaryUsageAllowed) != "null" || !bytes.Contains(body, []byte(`"ordinaryUsageAllowed":null`)) || bytes.Contains(body, []byte(`"ordinaryUsageAllowed":false`)) {
			t.Fatalf("explicit null lost %s", body)
		}
	})
	t.Run("false", func(t *testing.T) {
		report, body := must(t, quota(`,"ordinaryUsageAllowed":false`))
		if string(report.OrdinaryUsageAllowed) != "false" || !bytes.Contains(body, []byte(`"ordinaryUsageAllowed":false`)) {
			t.Fatalf("explicit false lost %s", body)
		}
	})
	t.Run("true", func(t *testing.T) {
		report, body := must(t, quota(`,"ordinaryUsageAllowed":true`))
		if string(report.OrdinaryUsageAllowed) != "true" || !bytes.Contains(body, []byte(`"ordinaryUsageAllowed":true`)) {
			t.Fatalf("explicit true lost %s", body)
		}
	})
	t.Run("spaced false", func(t *testing.T) {
		report, _ := must(t, quota(`,"ordinaryUsageAllowed": false`))
		if string(report.OrdinaryUsageAllowed) != "false" {
			t.Fatalf("boolean spelling changed %s", report.OrdinaryUsageAllowed)
		}
	})
	rejected := []struct{ name, extra string }{
		{"string false", `,"ordinaryUsageAllowed":"false"`},
		{"string true", `,"ordinaryUsageAllowed":"true"`},
		{"number zero", `,"ordinaryUsageAllowed":0`},
		{"number one", `,"ordinaryUsageAllowed":1`},
		{"object", `,"ordinaryUsageAllowed":{}`},
		{"array", `,"ordinaryUsageAllowed":[]`},
		{"duplicate", `,"ordinaryUsageAllowed":false,"ordinaryUsageAllowed":true`},
		{"case alias", `,"OrdinaryUsageAllowed":false`},
		{"invalid token", `,"ordinaryUsageAllowed":FALSE`},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := sanitizeCodexQuota(account, quota(tc.extra), at); err == nil {
				t.Fatal("unsupported ordinaryUsageAllowed accepted")
			}
		})
	}
	t.Run("bucket only", func(t *testing.T) {
		flagged := strings.Replace(base, `"secondary":null`, `"secondary":null,"ordinaryUsageAllowed":true`, 1)
		other := `{"limitId":"other-model","planType":"pro","primary":{"usedPercent":40},"ordinaryUsageAllowed":false}`
		raw := []byte(`{"rateLimits":` + flagged + `,"rateLimitsByLimitId":{"other-model":` + other + `,"codex":` + flagged + `}}`)
		_, body := must(t, raw)
		if bytes.Contains(body, []byte("ordinaryUsageAllowed")) || bytes.Contains(body, []byte("other-model")) {
			t.Fatal("bucket flag was copied onto the account capture")
		}
	})
	t.Run("top false beats per-model copies", func(t *testing.T) {
		flagged := strings.Replace(base, `"secondary":null`, `"secondary":null,"ordinaryUsageAllowed":true`, 1)
		raw := []byte(`{"ordinaryUsageAllowed":false,"rateLimits":` + flagged + `,"rateLimitsByLimitId":{"codex":` + flagged + `}}`)
		report, body := must(t, raw)
		if string(report.OrdinaryUsageAllowed) != "false" || bytes.Count(body, []byte(`"ordinaryUsageAllowed"`)) != 1 {
			t.Fatalf("flag multiplied or overwritten %s", body)
		}
	})
}
