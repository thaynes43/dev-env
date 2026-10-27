package v1alpha1_test

import (
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func schemaConversationUsage() *v1alpha1.ConversationUsageStatus {
	n, zero := int64(300), int64(0)
	unknown := v1alpha1.TokenObservationStatus{State: "Unknown"}
	return &v1alpha1.ConversationUsageStatus{
		Version: 1, Provider: "claude", ObservedAt: metav1.NewTime(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)),
		Coverage: "RegisteredConversation", CounterSemantics: "ConversationCumulative", ResultState: "Final", Turns: 3,
		Models: []v1alpha1.ConversationModelUsageStatus{{Model: "claude-opus-5-5",
			InputTokens: v1alpha1.TokenObservationStatus{State: "Reported", Amount: &n}, OutputTokens: v1alpha1.TokenObservationStatus{State: "Reported", Amount: &zero},
			CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown}},
	}
}

// conversationUsage is a separate status field: it never changes what
// usageObservation accepts, and its own rules are fixed words and bounds.
func TestConversationUsageSchema(t *testing.T) {
	c := ctx(t)
	s := taskSession()
	if err := k8s.Create(c, s); err != nil {
		t.Fatal(err)
	}
	s.Status.ConversationUsage = schemaConversationUsage()
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("valid conversation usage refused", err)
	}
	got := s.Status.ConversationUsage
	if len(got.Models) != 1 || got.Models[0].OutputTokens.Amount == nil || *got.Models[0].OutputTokens.Amount != 0 || got.Models[0].CacheReadInputTokens.Amount != nil {
		t.Fatal("Reported zero and Unknown were not both stored", got)
	}
	// It is replaced as agentd reports again; unlike the original task observation it is not immutable.
	s.Status.ConversationUsage.Turns = 4
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("a newer conversation usage was refused", err)
	}
	// Partial needs a reason and models; Unknown carries neither models nor zero.
	partial := schemaConversationUsage()
	partial.ResultState, partial.ReasonCode = "Partial", "skipped_records"
	unknown := schemaConversationUsage()
	unknown.ResultState, unknown.ReasonCode, unknown.Models, unknown.Turns = "Unknown", "no_usage", nil, 0
	for name, u := range map[string]*v1alpha1.ConversationUsageStatus{"partial": partial, "unknown": unknown} {
		s.Status.ConversationUsage = u
		if err := k8s.Status().Update(c, s); err != nil {
			t.Fatalf("%s refused: %v", name, err)
		}
	}
}

func TestConversationUsageSchemaRefusesWhatItDoesNotDefine(t *testing.T) {
	for name, mutate := range map[string]func(*v1alpha1.ConversationUsageStatus){
		"version 2":                func(u *v1alpha1.ConversationUsageStatus) { u.Version = 2 },
		"other provider":           func(u *v1alpha1.ConversationUsageStatus) { u.Provider = "opencode" },
		"the task coverage":        func(u *v1alpha1.ConversationUsageStatus) { u.Coverage = "InitialManagedInvocation" },
		"the task semantics":       func(u *v1alpha1.ConversationUsageStatus) { u.CounterSemantics = "InvocationFinalTotal" },
		"other result":             func(u *v1alpha1.ConversationUsageStatus) { u.ResultState = "Complete" },
		"final with a reason":      func(u *v1alpha1.ConversationUsageStatus) { u.ReasonCode = "skipped_records" },
		"partial without a reason": func(u *v1alpha1.ConversationUsageStatus) { u.ResultState = "Partial" },
		"reason not a word":        func(u *v1alpha1.ConversationUsageStatus) { u.ResultState, u.ReasonCode = "Partial", "Skipped Records!" },
		"unknown with models":      func(u *v1alpha1.ConversationUsageStatus) { u.ResultState, u.ReasonCode = "Unknown", "no_usage" },
		"final without models":     func(u *v1alpha1.ConversationUsageStatus) { u.Models = nil },
		"negative turns":           func(u *v1alpha1.ConversationUsageStatus) { u.Turns = -1 },
		"too many turns":           func(u *v1alpha1.ConversationUsageStatus) { u.Turns = 1_000_000_001 },
		"negative skipped":         func(u *v1alpha1.ConversationUsageStatus) { u.Skipped = -1 },
		"empty model":              func(u *v1alpha1.ConversationUsageStatus) { u.Models[0].Model = "" },
		"overlong model":           func(u *v1alpha1.ConversationUsageStatus) { u.Models[0].Model = strings.Repeat("m", 257) },
		"duplicate model":          func(u *v1alpha1.ConversationUsageStatus) { u.Models = append(u.Models, u.Models[0]) },
		"reported without amount":  func(u *v1alpha1.ConversationUsageStatus) { u.Models[0].InputTokens.Amount = nil },
		"unknown with an amount":   func(u *v1alpha1.ConversationUsageStatus) { n := int64(0); u.Models[0].CacheReadInputTokens.Amount = &n },
		"tokens over the bound": func(u *v1alpha1.ConversationUsageStatus) {
			n := int64(1_000_000_000_000_001)
			u.Models[0].InputTokens.Amount = &n
		},
		"negative tokens": func(u *v1alpha1.ConversationUsageStatus) { n := int64(-1); u.Models[0].InputTokens.Amount = &n },
		"too many models": func(u *v1alpha1.ConversationUsageStatus) {
			for i := 0; i < 8; i++ {
				m := u.Models[0]
				m.Model = "model-" + string(rune('a'+i))
				u.Models = append(u.Models, m)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := ctx(t)
			s := taskSession()
			if err := k8s.Create(c, s); err != nil {
				t.Fatal(err)
			}
			u := schemaConversationUsage()
			mutate(u)
			s.Status.ConversationUsage = u
			if err := k8s.Status().Update(c, s); !apierrors.IsInvalid(err) {
				t.Fatalf("an invalid conversation usage was accepted: %v", err)
			}
		})
	}
}

// Both status fields can be present and are independent.
func TestConversationUsageDoesNotChangeUsageObservation(t *testing.T) {
	c := ctx(t)
	s := taskSession()
	if err := k8s.Create(c, s); err != nil {
		t.Fatal(err)
	}
	s.Status.UsageObservation = schemaUsage()
	s.Status.ConversationUsage = schemaConversationUsage()
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("both usage fields refused", err)
	}
	s.Status.ConversationUsage.Turns = 9
	if err := k8s.Status().Update(c, s); err != nil {
		t.Fatal("changing the conversation figure was refused", err)
	}
	s.Status.UsageObservation.ResultState = "Partial"
	if err := k8s.Status().Update(c, s); !apierrors.IsInvalid(err) {
		t.Fatal("the original task observation became mutable", err)
	}
}
