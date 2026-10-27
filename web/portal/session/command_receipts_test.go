package session

import (
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/web/portal"
)

func TestCommandIDAndActorBinding(t *testing.T) {
	h := &commandHandler{}
	first, err := h.NewRequestID()
	if err != nil {
		t.Fatal("request ID failed")
	}
	second, err := h.NewRequestID()
	if err != nil || len(first) != 43 || !validCommandID(first) || first == second {
		t.Fatal("request IDs not distinct canonical values")
	}
	a, b := Handle{1}, Handle{2}
	p := portal.Principal{Issuer: "https://issuer.example/", Subject: "SECRET-SUBJECT"}
	one := actorRef(a, p)
	if one != actorRef(a, p) || one == actorRef(b, p) || len(one) != 43 || strings.Contains(one, p.Subject) || strings.Contains(one, p.Issuer) {
		t.Fatal("actor reference is not session-bound and opaque")
	}
	p.Subject = "other-subject"
	if one == actorRef(a, p) {
		t.Fatal("actor reference ignored subject")
	}
}
