package providerallowance_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const syntheticRetrievedAt = "2026-01-08T12:00:00Z"

const syntheticResetsAt = "2026-01-15T12:00:00Z"

const syntheticIncarnation = "synthetic-incarnation-1"

const syntheticSecretEcho = "SECRET-VALUE"

func TestCurrentSourceSyntheticFortySixty(t *testing.T) {
	report := decodeSynthetic(t, nil)
	if report.Schema != providerallowance.CurrentSourceSchema ||
		report.PublisherRef != providerallowance.CurrentSourcePublisher ||
		report.ProducerIncarnation != syntheticIncarnation ||
		report.SourceGeneration != 1 ||
		report.RetrievedAt != syntheticRetrievedAt ||
		report.ProviderUpdatedAt != nil ||
		report.UsedPercent != "40" ||
		report.RemainingPercent != "60" ||
		report.WindowDurationMinutes != 10080 ||
		report.ResetsAt != syntheticResetsAt {
		t.Fatalf("%+v", report)
	}
	requireCode(t, providerallowance.ValidateCurrentSource(report, happySelection(), happyNow()), nil)
}

func TestCurrentSourcePercentBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		used      string
		remaining string
		want      error
	}{
		{name: "zero and one hundred", used: "0", remaining: "100"},
		{name: "one hundred and zero", used: "100", remaining: "0"},
		{name: "zero point and one hundred point", used: "0.0", remaining: "100.00"},
		{name: "halves", used: "50", remaining: "50"},
		{name: "tenth complement", used: "0.1", remaining: "99.9"},
		{name: "quarter complement", used: "33.25", remaining: "66.75"},
		{name: "repeating complement", used: "33.333", remaining: "66.667"},
		{name: "lexical trailing zeros", used: "40.0", remaining: "60.00"},
		{name: "sum short", used: "40", remaining: "50", want: providerallowance.ErrCurrentSourcePercentSum},
		{name: "both zero", used: "0", remaining: "0", want: providerallowance.ErrCurrentSourcePercentSum},
		{name: "both one hundred", used: "100", remaining: "100", want: providerallowance.ErrCurrentSourcePercentSum},
		{name: "tenth not exact", used: "0.1", remaining: "99.8", want: providerallowance.ErrCurrentSourcePercentSum},
		{name: "repeating not exact", used: "33.333", remaining: "66.666", want: providerallowance.ErrCurrentSourcePercentSum},
		{name: "above one hundred", used: "100.0001", remaining: "0", want: providerallowance.ErrCurrentSourcePercentRange},
		{name: "one hundred one", used: "0", remaining: "101", want: providerallowance.ErrCurrentSourcePercentRange},
		{name: "exponent", used: "1e2", remaining: "0", want: providerallowance.ErrCurrentSourcePercentGrammar},
		{name: "exponent upper", used: "1E2", remaining: "0", want: providerallowance.ErrCurrentSourcePercentGrammar},
		{name: "leading zero", used: "01", remaining: "99", want: providerallowance.ErrCurrentSourcePercentGrammar},
		{name: "plus sign", used: "+40", remaining: "60", want: providerallowance.ErrCurrentSourcePercentGrammar},
		{name: "negative", used: "-1", remaining: "101", want: providerallowance.ErrCurrentSourcePercentGrammar},
		{name: "negative zero", used: "-0", remaining: "100", want: providerallowance.ErrCurrentSourcePercentGrammar},
		{name: "trailing dot", used: "40.", remaining: "60", want: providerallowance.ErrCurrentSourcePercentGrammar},
		{name: "leading dot", used: ".5", remaining: "99.5", want: providerallowance.ErrCurrentSourcePercentGrammar},
		{name: "blank", used: "", remaining: "100", want: providerallowance.ErrCurrentSourcePercentGrammar},
		{name: "space", used: " 40", remaining: "60", want: providerallowance.ErrCurrentSourcePercentGrammar},
		{name: "underscore", used: "4_0", remaining: "60", want: providerallowance.ErrCurrentSourcePercentGrammar},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := decodeSynthetic(t, func(doc map[string]any) {
				doc["usedPercent"] = tc.used
				doc["remainingPercent"] = tc.remaining
			})
			if report.UsedPercent != tc.used || report.RemainingPercent != tc.remaining {
				t.Fatal("lexical percent was rewritten")
			}
			requireCode(t, providerallowance.ValidateCurrentSource(report, happySelection(), happyNow()), tc.want)
		})
	}
}

func TestDecodeCurrentSourceClosedDocument(t *testing.T) {
	happy := syntheticJSON(t, nil)
	cases := []struct {
		name string
		raw  []byte
		want error
	}{
		{name: "empty", raw: []byte{}, want: providerallowance.ErrCurrentSourceJSON},
		{name: "whitespace", raw: []byte(" \n\t"), want: providerallowance.ErrCurrentSourceJSON},
		{name: "truncated", raw: []byte(`{"usedPercent":"` + syntheticSecretEcho), want: providerallowance.ErrCurrentSourceJSON},
		{name: "null", raw: []byte("null"), want: providerallowance.ErrCurrentSourceJSON},
		{name: "array", raw: []byte("[]"), want: providerallowance.ErrCurrentSourceJSON},
		{name: "string", raw: []byte(`"40"`), want: providerallowance.ErrCurrentSourceJSON},
		{name: "number", raw: []byte("40"), want: providerallowance.ErrCurrentSourceJSON},
		{name: "trailing comma", raw: []byte(`{"schema":"x",}`), want: providerallowance.ErrCurrentSourceJSON},
		{name: "bom", raw: append([]byte{0xEF, 0xBB, 0xBF}, happy...), want: providerallowance.ErrCurrentSourceJSON},
		{name: "invalid utf8", raw: []byte{0xff, '{', '}'}, want: providerallowance.ErrCurrentSourceJSON},
		{name: "empty object", raw: []byte("{}"), want: providerallowance.ErrCurrentSourceMissing},
		{name: "oversize", raw: bytes.Repeat([]byte("x"), 16*1024+1), want: providerallowance.ErrCurrentSourceSize},
		{name: "size boundary is not short", raw: bytes.Repeat([]byte("x"), 16*1024), want: providerallowance.ErrCurrentSourceJSON},
		{name: "trailing value", raw: append(append([]byte{}, happy...), []byte(" true")...), want: providerallowance.ErrCurrentSourceTrailing},
		{name: "second document", raw: append(append([]byte{}, happy...), happy...), want: providerallowance.ErrCurrentSourceTrailing},
		{name: "duplicate key", raw: bytes.Replace(happy, []byte(`"usedPercent":"40"`), []byte(`"usedPercent":"40","usedPercent":"41"`), 1), want: providerallowance.ErrCurrentSourceDuplicate},
		{name: "escaped duplicate", raw: []byte(`{"schema":"a","\u0073chema":"b"}`), want: providerallowance.ErrCurrentSourceDuplicate},
		{name: "casefold key", raw: bytes.Replace(happy, []byte(`"usedPercent"`), []byte(`"UsedPercent"`), 1), want: providerallowance.ErrCurrentSourceUnknown},
		{name: "folded alias beside canonical", raw: bytes.Replace(happy, []byte(`"usedPercent":"40"`), []byte(`"usedPercent":"40","UsedPercent":"40"`), 1), want: providerallowance.ErrCurrentSourceUnknown},
		{name: "long s fold", raw: bytes.Replace(happy, []byte(`"schema"`), []byte("\"\u017fchema\""), 1), want: providerallowance.ErrCurrentSourceUnknown},
		{name: "snake used percent", raw: bytes.Replace(happy, []byte(`"usedPercent"`), []byte(`"used_percent"`), 1), want: providerallowance.ErrCurrentSourceUnknown},
		{name: "unit key", raw: bytes.Replace(happy, []byte("{"), []byte(`{"unit":"percent",`), 1), want: providerallowance.ErrCurrentSourceUnknown},
		{name: "stable identity", raw: bytes.Replace(happy, []byte("{"), []byte(`{"accountRef":"`+syntheticSecretEcho+`","bindingState":"verified","accountIdentityStatus":"unavailable",`), 1), want: providerallowance.ErrCurrentSourceUnknown},
		{name: "numeric percent", raw: bytes.Replace(happy, []byte(`"usedPercent":"40"`), []byte(`"usedPercent":40`), 1), want: providerallowance.ErrCurrentSourceType},
		{name: "fractional percent", raw: bytes.Replace(happy, []byte(`"usedPercent":"40"`), []byte(`"usedPercent":40.0`), 1), want: providerallowance.ErrCurrentSourceType},
		{name: "bool generation", raw: bytes.Replace(happy, []byte(`"sourceGeneration":1`), []byte(`"sourceGeneration":true`), 1), want: providerallowance.ErrCurrentSourceType},
		{name: "string generation", raw: bytes.Replace(happy, []byte(`"sourceGeneration":1`), []byte(`"sourceGeneration":"1"`), 1), want: providerallowance.ErrCurrentSourceType},
		{name: "exponent generation", raw: bytes.Replace(happy, []byte(`"sourceGeneration":1`), []byte(`"sourceGeneration":1e2`), 1), want: providerallowance.ErrCurrentSourceType},
		{name: "overflow generation", raw: bytes.Replace(happy, []byte(`"sourceGeneration":1`), []byte(`"sourceGeneration":9223372036854775808`), 1), want: providerallowance.ErrCurrentSourceType},
		{name: "fractional duration", raw: bytes.Replace(happy, []byte(`"windowDurationMinutes":10080`), []byte(`"windowDurationMinutes":10080.0`), 1), want: providerallowance.ErrCurrentSourceType},
		{name: "object time", raw: bytes.Replace(happy, []byte(`"retrievedAt":"2026-01-08T12:00:00Z"`), []byte(`"retrievedAt":{}`), 1), want: providerallowance.ErrCurrentSourceType},
		{name: "array reset", raw: bytes.Replace(happy, []byte(`"resetsAt":"2026-01-15T12:00:00Z"`), []byte(`"resetsAt":[]`), 1), want: providerallowance.ErrCurrentSourceType},
		{name: "bool provider time", raw: bytes.Replace(happy, []byte(`"providerUpdatedAt":null`), []byte(`"providerUpdatedAt":false`), 1), want: providerallowance.ErrCurrentSourceType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report, err := providerallowance.DecodeCurrentSource(tc.raw)
			requireCode(t, err, tc.want)
			if report != (providerallowance.CurrentSourceReport{}) {
				t.Fatal("partial report")
			}
			if strings.Contains(err.Error(), syntheticSecretEcho) || strings.Contains(err.Error(), "accountRef") || strings.Contains(err.Error(), "usedPercent") {
				t.Fatalf("echoed input %q", err.Error())
			}
		})
	}

	nullable := []string{"schema", "publisherRef", "producerIncarnation", "sourceGeneration", "retrievedAt", "usedPercent", "remainingPercent", "windowDurationMinutes", "resetsAt"}
	for _, key := range nullable {
		t.Run("null "+key, func(t *testing.T) {
			raw := syntheticJSON(t, func(doc map[string]any) { doc[key] = nil })
			requireDecodeErr(t, raw, providerallowance.ErrCurrentSourceNull)
		})
	}
	for _, key := range append(append([]string{}, nullable...), "providerUpdatedAt") {
		t.Run("missing "+key, func(t *testing.T) {
			raw := syntheticJSON(t, func(doc map[string]any) { delete(doc, key) })
			requireDecodeErr(t, raw, providerallowance.ErrCurrentSourceMissing)
		})
	}
}

func TestDecodeCurrentSourceAcceptsShapeOnly(t *testing.T) {
	raw := syntheticJSON(t, func(doc map[string]any) {
		doc["sourceGeneration"] = int64(-1)
		doc["windowDurationMinutes"] = int64(0)
	})
	report, err := providerallowance.DecodeCurrentSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceGeneration != -1 || report.WindowDurationMinutes != 0 {
		t.Fatalf("generation %d duration %d", report.SourceGeneration, report.WindowDurationMinutes)
	}
	padded := append(append([]byte("\n"), raw...), bytes.Repeat([]byte(" "), 16*1024-len(raw)-1)...)
	if len(padded) != 16*1024 {
		t.Fatalf("padded length %d", len(padded))
	}
	if _, err = providerallowance.DecodeCurrentSource(padded); err != nil {
		t.Fatal(err)
	}
	if _, err = providerallowance.DecodeCurrentSource(append(padded, ' ')); !errors.Is(err, providerallowance.ErrCurrentSourceSize) {
		t.Fatal(err)
	}
	var decoded providerallowance.CurrentSourceReport
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SourceGeneration != -1 {
		t.Fatal(decoded.SourceGeneration)
	}
	folded := bytes.Replace(syntheticJSON(t, nil), []byte(`"schema"`), []byte(`"Schema"`), 1)
	if err = json.Unmarshal(folded, &decoded); !errors.Is(err, providerallowance.ErrCurrentSourceUnknown) {
		t.Fatal(err)
	}
}

func TestCurrentSourceKeyOrderAndProviderOffset(t *testing.T) {
	raw := []byte(`{
		"windowDurationMinutes": 10080,
		"usedPercent": "40",
		"sourceGeneration": 1,
		"schema": "codex.get_usage_limits.current_source.v1",
		"retrievedAt": "2026-01-08T07:00:00-05:00",
		"resetsAt": "2026-01-15T12:00:00Z",
		"remainingPercent": "60",
		"remainingPercentBasis": "derived_complement_of_used_percent",
		"publisherRef": "codex-get-usage-limits-observation-publisher",
		"providerUpdatedAt": "2026-01-08T12:00:00Z",
		"producerIncarnation": "synthetic-incarnation-1"
	}`)
	report, err := providerallowance.DecodeCurrentSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	if report.RetrievedAt != "2026-01-08T07:00:00-05:00" || report.ProviderUpdatedAt == nil || *report.ProviderUpdatedAt != "2026-01-08T12:00:00Z" {
		t.Fatalf("%+v", report)
	}
	requireCode(t, providerallowance.ValidateCurrentSource(report, happySelection(), happyNow()), nil)
}

func TestValidateCurrentSourceRules(t *testing.T) {
	retrieved := time.Date(2026, 1, 8, 12, 0, 0, 0, time.UTC)
	resetBoundary := time.Date(2026, 1, 8, 18, 0, 0, 0, time.UTC)
	zero := time.Time{}
	cases := []struct {
		name   string
		now    *time.Time
		mutate func(*providerallowance.CurrentSourceReport, *providerallowance.CurrentSourceSelection)
		want   error
	}{
		{name: "zero now", now: &zero, want: providerallowance.ErrCurrentSourceNow},
		{name: "schema switch", mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
			report.Schema = "codex.get_usage_limits.current_source.v2"
		}, want: providerallowance.ErrCurrentSourceSchema},
		{name: "publisher switch", mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
			report.PublisherRef = "other-publisher"
		}, want: providerallowance.ErrCurrentSourcePublisher},
		{name: "matched foreign publisher", mutate: func(report *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			report.PublisherRef = "other-publisher"
			selected.PublisherRef = "other-publisher"
		}, want: providerallowance.ErrCurrentSourcePublisher},
		{name: "empty incarnation", mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
			report.ProducerIncarnation = ""
		}, want: providerallowance.ErrCurrentSourceIdentifier},
		{name: "spaced incarnation", mutate: func(report *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			report.ProducerIncarnation = "synthetic incarnation"
			selected.ProducerIncarnation = report.ProducerIncarnation
		}, want: providerallowance.ErrCurrentSourceIdentifier},
		{name: "unicode incarnation", mutate: func(report *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			report.ProducerIncarnation = "synthetic-\u00e9"
			selected.ProducerIncarnation = report.ProducerIncarnation
		}, want: providerallowance.ErrCurrentSourceIdentifier},
		{name: "incarnation 129", mutate: func(report *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			report.ProducerIncarnation = strings.Repeat("a", 129)
			selected.ProducerIncarnation = report.ProducerIncarnation
		}, want: providerallowance.ErrCurrentSourceIdentifier},
		{name: "incarnation 128", mutate: func(report *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			report.ProducerIncarnation = strings.Repeat("b", 128)
			selected.ProducerIncarnation = report.ProducerIncarnation
		}},
		{name: "empty selection publisher", mutate: func(_ *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			selected.PublisherRef = ""
		}, want: providerallowance.ErrCurrentSourceIdentifier},
		{name: "generation zero", mutate: func(report *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			report.SourceGeneration = 0
			selected.SourceGeneration = 0
		}, want: providerallowance.ErrCurrentSourceGeneration},
		{name: "generation negative", mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
			report.SourceGeneration = -1
		}, want: providerallowance.ErrCurrentSourceGeneration},
		{name: "incarnation switch", mutate: func(_ *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			selected.ProducerIncarnation = "synthetic-incarnation-2"
		}, want: providerallowance.ErrCurrentSourceSelection},
		{name: "generation switch", mutate: func(_ *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			selected.SourceGeneration = 2
		}, want: providerallowance.ErrCurrentSourceSelection},
		{name: "selection publisher switch", mutate: func(_ *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			selected.PublisherRef = "other-publisher"
		}, want: providerallowance.ErrCurrentSourceSelection},
		{name: "matched other incarnation", mutate: func(report *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			report.ProducerIncarnation = "synthetic-incarnation-2"
			selected.ProducerIncarnation = "synthetic-incarnation-2"
		}},
		{name: "matched other generation", mutate: func(report *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			report.SourceGeneration = 2
			selected.SourceGeneration = 2
		}},
		{name: "max age zero", mutate: func(_ *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			selected.MaxAge = 0
		}, want: providerallowance.ErrCurrentSourceMaxAge},
		{name: "max age negative", mutate: func(_ *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			selected.MaxAge = -time.Minute
		}, want: providerallowance.ErrCurrentSourceMaxAge},
		{name: "max age over a day", mutate: func(_ *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
			selected.MaxAge = 24*time.Hour + time.Nanosecond
		}, want: providerallowance.ErrCurrentSourceMaxAge},
		{name: "lowercase t", mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
			report.RetrievedAt = "2026-01-08t12:00:00Z"
		}, want: providerallowance.ErrCurrentSourceTime},
		{name: "lowercase z", mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
			report.RetrievedAt = "2026-01-08T12:00:00z"
		}, want: providerallowance.ErrCurrentSourceTime},
		{name: "date only", mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
			report.ResetsAt = "2026-01-15"
		}, want: providerallowance.ErrCurrentSourceTime},
		{name: "provider blank", mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
			blank := ""
			report.ProviderUpdatedAt = &blank
		}, want: providerallowance.ErrCurrentSourceTime},
		{
			name: "future retrieval",
			now:  timePtr(retrieved.Add(10 * time.Minute)),
			mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
				report.RetrievedAt = "2026-01-08T12:10:00.000000001Z"
			},
			want: providerallowance.ErrCurrentSourceFutureRetrieval,
		},
		{
			name: "retrieved one nanosecond ahead",
			now:  timePtr(retrieved.Add(-time.Nanosecond)),
			want: providerallowance.ErrCurrentSourceFutureRetrieval,
		},
		{
			name: "now equals retrieved",
			now:  timePtr(retrieved),
		},
		{
			name: "future provider",
			now:  timePtr(retrieved.Add(10 * time.Minute)),
			mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
				updated := "2026-01-08T12:10:00.000000001Z"
				report.ProviderUpdatedAt = &updated
			},
			want: providerallowance.ErrCurrentSourceFutureProvider,
		},
		{
			name: "provider after retrieval",
			mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
				updated := "2026-01-08T12:05:00Z"
				report.ProviderUpdatedAt = &updated
			},
			want: providerallowance.ErrCurrentSourceProviderAfterRetrieval,
		},
		{
			name: "provider equal retrieved",
			mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
				updated := syntheticRetrievedAt
				report.ProviderUpdatedAt = &updated
			},
		},
		{
			name: "reset equal retrieved",
			mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
				report.ResetsAt = syntheticRetrievedAt
			},
			want: providerallowance.ErrCurrentSourceResetOrder,
		},
		{
			name: "reset before retrieved",
			mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
				report.ResetsAt = "2026-01-08T11:00:00Z"
			},
			want: providerallowance.ErrCurrentSourceResetOrder,
		},
		{
			name: "reset equal now",
			now:  timePtr(resetBoundary),
			mutate: func(report *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
				report.ResetsAt = "2026-01-08T18:00:00Z"
				selected.MaxAge = 24 * time.Hour
			},
			want: providerallowance.ErrCurrentSourceReset,
		},
		{
			name: "reset one nanosecond ahead",
			now:  timePtr(resetBoundary.Add(-time.Nanosecond)),
			mutate: func(report *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
				report.ResetsAt = "2026-01-08T18:00:00Z"
				selected.MaxAge = 24 * time.Hour
			},
		},
		{name: "duration zero", mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
			report.WindowDurationMinutes = 0
		}, want: providerallowance.ErrCurrentSourceDuration},
		{name: "duration negative", mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
			report.WindowDurationMinutes = -1
		}, want: providerallowance.ErrCurrentSourceDuration},
		{name: "duration one", mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
			report.WindowDurationMinutes = 1
		}},
		{name: "duration year", mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
			report.WindowDurationMinutes = 525600
		}},
		{name: "duration over year", mutate: func(report *providerallowance.CurrentSourceReport, _ *providerallowance.CurrentSourceSelection) {
			report.WindowDurationMinutes = 525601
		}, want: providerallowance.ErrCurrentSourceDuration},
		{
			name: "max age equal",
			now:  timePtr(retrieved.Add(30 * time.Minute)),
		},
		{
			name: "max age one nanosecond over",
			now:  timePtr(retrieved.Add(30*time.Minute + time.Nanosecond)),
			want: providerallowance.ErrCurrentSourceExpired,
		},
		{
			name: "age equal to day cap",
			now:  timePtr(retrieved.Add(24 * time.Hour)),
			mutate: func(_ *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
				selected.MaxAge = 24 * time.Hour
			},
		},
		{
			name: "age over day cap",
			now:  timePtr(retrieved.Add(24*time.Hour + time.Nanosecond)),
			mutate: func(_ *providerallowance.CurrentSourceReport, selected *providerallowance.CurrentSourceSelection) {
				selected.MaxAge = 24 * time.Hour
			},
			want: providerallowance.ErrCurrentSourceExpired,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := decodeSynthetic(t, nil)
			selected := happySelection()
			now := happyNow()
			if tc.now != nil {
				now = *tc.now
			}
			if tc.mutate != nil {
				tc.mutate(&report, &selected)
			}
			beforeUsed, beforeRemaining := report.UsedPercent, report.RemainingPercent
			err := providerallowance.ValidateCurrentSource(report, selected, now)
			requireCode(t, err, tc.want)
			if report.UsedPercent != beforeUsed || report.RemainingPercent != beforeRemaining {
				t.Fatal("percents were rewritten")
			}
		})
	}
}

func TestCurrentSourceHasNoStableIdentity(t *testing.T) {
	reportType := reflect.TypeOf(providerallowance.CurrentSourceReport{})
	selectionType := reflect.TypeOf(providerallowance.CurrentSourceSelection{})
	if reportType.NumField() != 11 || selectionType.NumField() != 4 {
		t.Fatalf("fields report=%d selection=%d", reportType.NumField(), selectionType.NumField())
	}
	tags := []string{
		"schema", "publisherRef", "producerIncarnation", "sourceGeneration", "retrievedAt",
		"providerUpdatedAt", "usedPercent", "remainingPercent", "windowDurationMinutes", "resetsAt", "remainingPercentBasis",
	}
	for i, tag := range tags {
		field := reportType.Field(i)
		if string(field.Tag) != `json:"`+tag+`"` {
			t.Fatalf("field %d tag %s", i, field.Tag)
		}
		lower := strings.ToLower(field.Name)
		if strings.Contains(lower, "account") || strings.Contains(lower, "binding") {
			t.Fatal(field.Name)
		}
	}
	usedPercent := mustField(t, reportType, "UsedPercent")
	remainingPercent := mustField(t, reportType, "RemainingPercent")
	generation := mustField(t, reportType, "SourceGeneration")
	duration := mustField(t, reportType, "WindowDurationMinutes")
	providerUpdated := mustField(t, reportType, "ProviderUpdatedAt")
	if usedPercent.Type.Kind() != reflect.String || remainingPercent.Type.Kind() != reflect.String || generation.Type.Kind() != reflect.Int64 || duration.Type.Kind() != reflect.Int64 || providerUpdated.Type != reflect.TypeOf((*string)(nil)) {
		t.Fatal("field type")
	}
	for i := 0; i < selectionType.NumField(); i++ {
		field := selectionType.Field(i)
		if field.Tag != "" {
			t.Fatal(field.Tag)
		}
		lower := strings.ToLower(field.Name)
		if strings.Contains(lower, "account") || strings.Contains(lower, "binding") {
			t.Fatal(field.Name)
		}
	}
	if mustField(t, selectionType, "MaxAge").Type != reflect.TypeOf(time.Duration(0)) {
		t.Fatal("max age type")
	}
	bound := reflect.TypeOf(providerallowance.CurrentSourceReport{})
	for _, sample := range []any{
		providerallowance.ReadItem{},
		providerallowance.ReadResponse{},
		providerallowance.Observation{},
		providerallowance.Measure{},
		providerallowance.Scope{},
	} {
		typ := reflect.TypeOf(sample)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Type == bound || strings.Contains(strings.ToLower(field.Name), "currentsource") {
				t.Fatalf("%s.%s", typ.Name(), field.Name)
			}
		}
	}
}

func syntheticJSON(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	doc := map[string]any{
		"schema":                providerallowance.CurrentSourceSchema,
		"publisherRef":          providerallowance.CurrentSourcePublisher,
		"producerIncarnation":   syntheticIncarnation,
		"sourceGeneration":      int64(1),
		"retrievedAt":           syntheticRetrievedAt,
		"providerUpdatedAt":     nil,
		"usedPercent":           "40",
		"remainingPercent":      "60",
		"remainingPercentBasis": providerallowance.CurrentSourceRemainingBasis,
		"windowDurationMinutes": int64(10080),
		"resetsAt":              syntheticResetsAt,
	}
	if mutate != nil {
		mutate(doc)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeSynthetic(t *testing.T, mutate func(map[string]any)) providerallowance.CurrentSourceReport {
	t.Helper()
	report, err := providerallowance.DecodeCurrentSource(syntheticJSON(t, mutate))
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func happySelection() providerallowance.CurrentSourceSelection {
	return providerallowance.CurrentSourceSelection{
		PublisherRef:        providerallowance.CurrentSourcePublisher,
		ProducerIncarnation: syntheticIncarnation,
		SourceGeneration:    1,
		MaxAge:              30 * time.Minute,
	}
}

func happyNow() time.Time {
	return time.Date(2026, 1, 8, 12, 10, 0, 0, time.UTC)
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func mustField(t *testing.T, typ reflect.Type, name string) reflect.StructField {
	t.Helper()
	field, ok := typ.FieldByName(name)
	if !ok {
		t.Fatalf("missing %s", name)
	}
	return field
}

func requireDecodeErr(t *testing.T, raw []byte, want error) {
	t.Helper()
	report, err := providerallowance.DecodeCurrentSource(raw)
	requireCode(t, err, want)
	if report != (providerallowance.CurrentSourceReport{}) {
		t.Fatal("partial report")
	}
}

func requireCode(t *testing.T, err, want error) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	if err == nil || !errors.Is(err, want) || err.Error() != want.Error() {
		t.Fatalf("got %v want %v", err, want)
	}
	if strings.ContainsAny(err.Error(), " \t\r\n\"'`") {
		t.Fatalf("unsanitized code %q", err.Error())
	}
}

func TestCurrentSourceRemainingMustBeDerived(t *testing.T) {
	report := decodeSynthetic(t, nil)
	report.RemainingPercentBasis = "provider_reported"
	requireCode(t, providerallowance.ValidateCurrentSource(report, happySelection(), happyNow()), providerallowance.ErrCurrentSourceRemainingBasis)
}
