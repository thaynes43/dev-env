package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/templates"
)

const (
	launchActor   = "actor-000000000001"
	launchRequest = "request-0000000001"
	launchPrompt  = "SECRET-LAUNCH-PROMPT fix the docs"
)

// launchFixture is a server whose portal ServiceAccount token is bound to a real
// pod in the fake client. repos turns the launch route on; nil leaves it off.
func launchFixture(t *testing.T, repos ...string) *fixture {
	t.Helper()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: sessionNS, UID: types.UID("portal-pod-uid")}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-v2-portal", Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
	f := newFixture(t, pod)
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth["tok-portal"] = id
	f.srv.Policy.PortalReaderEnabled = true
	if len(repos) > 0 {
		f.srv.PortalLaunchProfile = "dev"
		f.srv.PortalLaunchMaxUnfinished = apiv1.MaxPortalLaunchMaxUnfinished
		f.srv.PortalLaunchRepos = map[string]struct{}{}
		for _, r := range repos {
			f.srv.PortalLaunchRepos[r] = struct{}{}
		}
	}
	f.h = f.srv.Handler()
	return f
}

func launchTask() apiv1.PortalLaunchRequest {
	return apiv1.PortalLaunchRequest{RequestID: launchRequest, ActorRef: launchActor, Repo: "dev-env", Mode: "task", Model: "claude-opus-5-5", Effort: "xhigh", Size: "S", Prompt: launchPrompt}
}

func sessionsIn(t *testing.T, f *fixture) []v1alpha1.AgentSession {
	t.Helper()
	var list v1alpha1.AgentSessionList
	if err := f.c.List(context.Background(), &list, client.InNamespace(sessionNS)); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func TestPortalLaunchRouteDefaultOff(t *testing.T) {
	f := launchFixture(t)
	for _, tok := range []string{"tok-portal", tokHuman, tokClient, ""} {
		if w := f.do(http.MethodPost, apiv1.PortalLaunchPath, tok, launchTask()); w.Code != http.StatusNotFound {
			t.Fatalf("default-off route answered %d for %q", w.Code, tok)
		}
	}
	// The portal reader still cannot create through the generic route.
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, "tok-portal", apiv1.CreateSessionRequest{Repo: "dev-env", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "x"}), http.StatusForbidden, apiv1.CodeForbidden)
	if n := len(sessionsIn(t, f)); n != 0 {
		t.Fatalf("%d sessions created", n)
	}
	// With the route on, the generic create stays refused for the portal reader.
	f = launchFixture(t, "dev-env")
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, "tok-portal", apiv1.CreateSessionRequest{Repo: "dev-env", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "x"}), http.StatusForbidden, apiv1.CodeForbidden)
	// A route that is on allows only POST of this exact path.
	wantError(t, f.do(http.MethodGet, apiv1.PortalLaunchPath, "tok-portal", nil), http.StatusMethodNotAllowed, apiv1.CodeMethodNotAllowed)
}

func TestPortalLaunchOnlyThePortalReaderMayCall(t *testing.T) {
	f := launchFixture(t, "dev-env")
	for _, tok := range []string{tokHuman, tokClient, tokStranger} {
		if w := f.do(http.MethodPost, apiv1.PortalLaunchPath, tok, launchTask()); w.Code != http.StatusForbidden {
			t.Fatalf("token %s answered %d", tok, w.Code)
		}
	}
	if w := f.do(http.MethodPost, apiv1.PortalLaunchPath, "", launchTask()); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous answered %d", w.Code)
	}
	// The handler itself refuses every other caller kind, so a route wired wrongly
	// would still not serve them.
	body, _ := json.Marshal(launchTask())
	for _, kind := range []callerKind{kindHuman, kindClient, kindSession, kindCoordinator, kindPortalLaunch} {
		r := httptest.NewRequest(http.MethodPost, apiv1.PortalLaunchPath, strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		if _, _, err := f.srv.portalLaunch(context.Background(), httptest.NewRecorder(), r, &caller{kind: kind}); err == nil {
			t.Fatalf("%s created a session through the portal route", kind)
		}
	}
	if n := len(sessionsIn(t, f)); n != 0 {
		t.Fatalf("%d sessions created", n)
	}
}

func TestPortalLaunchCreatesExactlyTheConstrainedSession(t *testing.T) {
	f := launchFixture(t, "dev-env", "haynes-ops")
	w := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", launchTask())
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	receipt := decode[apiv1.PortalLaunchReceipt](t, w)
	items := sessionsIn(t, f)
	if len(items) != 1 {
		t.Fatalf("%d sessions", len(items))
	}
	s := items[0]
	if receipt.RequestID != launchRequest || receipt.SessionName != s.Name || receipt.SessionUID != string(s.UID) || receipt.Phase == "" || !receipt.ObservedAt.Equal(f.now) {
		t.Fatalf("receipt %+v for %s", receipt, s.Name)
	}
	if w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "SECRET-LAUNCH-PROMPT") {
		t.Fatalf("response leaked a route or the prompt: %v %s", w.Header(), w.Body.String())
	}
	spec := s.Spec
	if spec.Repo != "dev-env" || spec.Agent != v1alpha1.AgentClaude || spec.Mode != v1alpha1.ModeTask || spec.Model != "claude-opus-5-5" || spec.Effort != "xhigh" || spec.Size != v1alpha1.SizeS || spec.Prompt != launchPrompt || spec.Parent != "portal/"+launchActor {
		t.Fatalf("spec %+v", spec)
	}
	// Nothing else is set: no profile (the templates' default), project, base,
	// tools, limits, lifecycle, restore, workspace, canary or caller.
	empty := v1alpha1.AgentSessionSpec{Repo: spec.Repo, Agent: spec.Agent, Mode: spec.Mode, Model: spec.Model, Effort: spec.Effort, Size: spec.Size, Prompt: spec.Prompt, Parent: spec.Parent, OperatingMode: spec.OperatingMode, Profile: "dev"}
	if spec.Profile != "dev" || spec.Base != "" || len(spec.Tools) != 0 || spec.Limits != nil || spec.Lifecycle != nil || spec.Restore != "" || spec.Workspace != nil || spec.ProjectCanary != nil || spec.Caller != "" || spec.Lane != "" || spec.CommonRules != nil || spec.PrivatePilot != nil || spec.UsageCaptureParentUID != "" || spec.SessionDirectory != nil || !equalSpec(spec, empty) {
		t.Fatalf("spec carries more than the launch fields: %+v", spec)
	}
	if s.Annotations[v1alpha1.AnnotationProjectSnapshot] != "" || s.Annotations[TaskBudgetUIDAnnotation] != "" {
		t.Fatalf("annotations %v", s.Annotations)
	}
	if !regexp.MustCompile(`^portal-[0-9a-f]{40}$`).MatchString(s.Labels[v1alpha1.LabelIdempotencyKey]) || s.Labels[v1alpha1.LabelDepth] != "1" {
		t.Fatalf("labels %v", s.Labels)
	}
	if !regexp.MustCompile(apiv1.PortalLauncherPattern).MatchString(spec.Parent) {
		t.Fatalf("parent %q would not display as launchedBy", spec.Parent)
	}
	if a := portalActivity(&s, f.now); a.LaunchedBy != "portal/"+launchActor {
		t.Fatalf("launchedBy %q", a.LaunchedBy)
	}

	// A local and a remote session carry no prompt, and size and effort are optional.
	for _, mode := range []string{"local", "remote"} {
		req := apiv1.PortalLaunchRequest{RequestID: "request-" + mode + "-000001", ActorRef: launchActor, Repo: "haynes-ops", Mode: mode, Model: "claude-opus-5-5"}
		if w := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", req); w.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", mode, w.Code, w.Body.String())
		}
	}
	if n := len(sessionsIn(t, f)); n != 3 {
		t.Fatalf("%d sessions", n)
	}
}

func equalSpec(a, b v1alpha1.AgentSessionSpec) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestPortalLaunchIsIdempotentPerRequestAndActor(t *testing.T) {
	f := launchFixture(t, "dev-env")
	first := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", launchTask())
	again := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", launchTask())
	if first.Code != http.StatusCreated || again.Code != http.StatusOK {
		t.Fatalf("first %d, repeat %d %s", first.Code, again.Code, again.Body.String())
	}
	a, b := decode[apiv1.PortalLaunchReceipt](t, first), decode[apiv1.PortalLaunchReceipt](t, again)
	if a.SessionName != b.SessionName || a.SessionUID != b.SessionUID || len(sessionsIn(t, f)) != 1 {
		t.Fatalf("repeat made a second session: %+v %+v", a, b)
	}
	// The same request ID with a different request is a conflict, not a new session.
	changed := launchTask()
	changed.Model = "claude-sonnet-5-5"
	wantError(t, f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", changed), http.StatusConflict, apiv1.CodeConflict)
	// A different request ID is a new session.
	other := launchTask()
	other.RequestID = "request-0000000002"
	if w := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", other); w.Code != http.StatusCreated || len(sessionsIn(t, f)) != 2 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// Dedupe is per parent: another actor with the same request ID is its own launch.
	third := launchTask()
	third.ActorRef = "actor-000000000002"
	if w := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", third); w.Code != http.StatusCreated || len(sessionsIn(t, f)) != 3 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestPortalLaunchRefusesWhatItMayNotCreate(t *testing.T) {
	f := launchFixture(t, "dev-env")
	edit := func(f func(*apiv1.PortalLaunchRequest)) apiv1.PortalLaunchRequest {
		r := launchTask()
		f(&r)
		return r
	}
	for name, tc := range map[string]struct {
		req  apiv1.PortalLaunchRequest
		code int
	}{
		"repo not allowed":     {edit(func(r *apiv1.PortalLaunchRequest) { r.Repo = "haynes-ops" }), http.StatusForbidden},
		"repo with a path":     {edit(func(r *apiv1.PortalLaunchRequest) { r.Repo = "../dev-env" }), http.StatusForbidden},
		"short request id":     {edit(func(r *apiv1.PortalLaunchRequest) { r.RequestID = "short" }), http.StatusForbidden},
		"bad actor":            {edit(func(r *apiv1.PortalLaunchRequest) { r.ActorRef = "has space 000000000" }), http.StatusForbidden},
		"slash in actor":       {edit(func(r *apiv1.PortalLaunchRequest) { r.ActorRef = "actor/00000000000001" }), http.StatusForbidden},
		"mode empty":           {edit(func(r *apiv1.PortalLaunchRequest) { r.Mode = "" }), http.StatusUnprocessableEntity},
		"mode other":           {edit(func(r *apiv1.PortalLaunchRequest) { r.Mode = "batch" }), http.StatusUnprocessableEntity},
		"size L":               {edit(func(r *apiv1.PortalLaunchRequest) { r.Size = "L" }), http.StatusUnprocessableEntity},
		"size lowercase":       {edit(func(r *apiv1.PortalLaunchRequest) { r.Size = "s" }), http.StatusUnprocessableEntity},
		"task without prompt":  {edit(func(r *apiv1.PortalLaunchRequest) { r.Prompt = "" }), http.StatusUnprocessableEntity},
		"task blank prompt":    {edit(func(r *apiv1.PortalLaunchRequest) { r.Prompt = " \n\t" }), http.StatusUnprocessableEntity},
		"local with prompt":    {edit(func(r *apiv1.PortalLaunchRequest) { r.Mode = "local" }), http.StatusUnprocessableEntity},
		"remote with prompt":   {edit(func(r *apiv1.PortalLaunchRequest) { r.Mode = "remote" }), http.StatusUnprocessableEntity},
		"model alias opus":     {edit(func(r *apiv1.PortalLaunchRequest) { r.Model = "opus" }), http.StatusUnprocessableEntity},
		"model alias sonnet":   {edit(func(r *apiv1.PortalLaunchRequest) { r.Model = "sonnet" }), http.StatusUnprocessableEntity},
		"model alias fable":    {edit(func(r *apiv1.PortalLaunchRequest) { r.Model = "fable" }), http.StatusUnprocessableEntity},
		"model alias haiku":    {edit(func(r *apiv1.PortalLaunchRequest) { r.Model = "Haiku" }), http.StatusUnprocessableEntity},
		"model alias default":  {edit(func(r *apiv1.PortalLaunchRequest) { r.Model = "default" }), http.StatusUnprocessableEntity},
		"model alias 1m":       {edit(func(r *apiv1.PortalLaunchRequest) { r.Model = "opus[1m]" }), http.StatusUnprocessableEntity},
		"model latest":         {edit(func(r *apiv1.PortalLaunchRequest) { r.Model = "claude-opus-latest" }), http.StatusUnprocessableEntity},
		"model no version":     {edit(func(r *apiv1.PortalLaunchRequest) { r.Model = "claude-opus" }), http.StatusUnprocessableEntity},
		"model with a space":   {edit(func(r *apiv1.PortalLaunchRequest) { r.Model = "claude-opus-5-5 x" }), http.StatusUnprocessableEntity},
		"model empty":          {edit(func(r *apiv1.PortalLaunchRequest) { r.Model = "" }), http.StatusUnprocessableEntity},
		"model unknown effort": {edit(func(r *apiv1.PortalLaunchRequest) { r.Effort = "turbo" }), http.StatusUnprocessableEntity},
		"prompt too large":     {edit(func(r *apiv1.PortalLaunchRequest) { r.Prompt = strings.Repeat("p", 64<<10+1) }), http.StatusUnprocessableEntity},
	} {
		w := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", tc.req)
		if w.Code != tc.code {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "SECRET-LAUNCH-PROMPT") || strings.Contains(w.Body.String(), "ppppp") {
			t.Errorf("%s: the response echoed the prompt", name)
		}
	}
	if n := len(sessionsIn(t, f)); n != 0 {
		t.Fatalf("%d sessions created by refused requests", n)
	}
}

func TestPortalLaunchDecodeIsStrict(t *testing.T) {
	f := launchFixture(t, "dev-env")
	good := `"requestID":"` + launchRequest + `","actorRef":"` + launchActor + `","repo":"dev-env","mode":"remote","model":"claude-opus-5-5"`
	if w := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", `{`+good+`}`); w.Code != http.StatusCreated {
		t.Fatalf("baseline %d %s", w.Code, w.Body.String())
	}
	for name, body := range map[string]string{
		"agent":          `{` + good + `,"agent":"codex"}`,
		"project":        `{` + good + `,"project":"dev-env"}`,
		"base":           `{` + good + `,"base":"origin/main"}`,
		"profile":        `{` + good + `,"profile":"ops"}`,
		"tools":          `{` + good + `,"tools":["blender"]}`,
		"limits":         `{` + good + `,"limits":{"maxTurns":1}}`,
		"lifecycle":      `{` + good + `,"lifecycle":{"idleSuspendAfter":"1h"}}`,
		"restore":        `{` + good + `,"restore":"a/b"}`,
		"canary":         `{` + good + `,"canaryID":"x"}`,
		"idempotency":    `{` + good + `,"idempotencyKey":"mine"}`,
		"name":           `{` + good + `,"name":"x"}`,
		"duplicate key":  `{` + good + `,"mode":"local"}`,
		"missing model":  `{"requestID":"` + launchRequest + `","actorRef":"` + launchActor + `","repo":"dev-env","mode":"remote"}`,
		"missing repo":   `{"requestID":"` + launchRequest + `","actorRef":"` + launchActor + `","mode":"remote","model":"claude-opus-5-5"}`,
		"number as text": `{"requestID":1,"actorRef":"` + launchActor + `","repo":"dev-env","mode":"remote","model":"claude-opus-5-5"}`,
		"null requestID": `{"requestID":null,"actorRef":"` + launchActor + `","repo":"dev-env","mode":"remote","model":"claude-opus-5-5"}`,
		"null actorRef":  `{"requestID":"` + launchRequest + `","actorRef":null,"repo":"dev-env","mode":"remote","model":"claude-opus-5-5"}`,
		"null repo":      `{"requestID":"` + launchRequest + `","actorRef":"` + launchActor + `","repo":null,"mode":"remote","model":"claude-opus-5-5"}`,
		"null mode":      `{"requestID":"` + launchRequest + `","actorRef":"` + launchActor + `","repo":"dev-env","mode":null,"model":"claude-opus-5-5"}`,
		"null model":     `{"requestID":"` + launchRequest + `","actorRef":"` + launchActor + `","repo":"dev-env","mode":"remote","model":null}`,
		"null effort":    `{` + good + `,"effort":null}`,
		"null size":      `{` + good + `,"size":null}`,
		"null prompt":    `{` + good + `,"prompt":null}`,
		"trailing value": `{` + good + `}{}`,
		"not an object":  `[]`,
		"empty":          ``,
	} {
		w := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", body)
		if w.Code == http.StatusCreated || w.Code == http.StatusOK {
			t.Errorf("%s was accepted", name)
		}
	}
	// A wrong content type is refused, and so is a query string.
	r := httptest.NewRequest(http.MethodPost, apiv1.PortalLaunchPath, strings.NewReader(`{`+good+`}`))
	r.Header.Set("Authorization", "Bearer tok-portal")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	if w.Code == http.StatusCreated {
		t.Error("a body without a JSON content type was accepted")
	}
	if w := f.do(http.MethodPost, apiv1.PortalLaunchPath+"?profile=ops", "tok-portal", `{`+good+`}`); w.Code == http.StatusCreated {
		t.Error("a query string was accepted")
	}
	if n := len(sessionsIn(t, f)); n != 1 {
		t.Fatalf("%d sessions: only the baseline may exist", n)
	}
}

func TestPortalLaunchParentCannotBeAnotherCaller(t *testing.T) {
	f := launchFixture(t, "dev-env")
	f.srv.Policy.Clients = append(f.srv.Policy.Clients, "portal/"+launchActor)
	if w := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", launchTask()); w.Code != http.StatusForbidden || len(sessionsIn(t, f)) != 0 {
		t.Fatalf("a parent equal to a client was accepted: %d", w.Code)
	}
}

// The launch parent is a name no session can have, and the derived caller has no
// profile to hand on: a session it launches runs on the templates' default.
func TestPortalLaunchCallerHasNoOtherAuthority(t *testing.T) {
	f := launchFixture(t, "dev-env")
	launcher := &caller{kind: kindPortalLaunch, parent: "portal/" + launchActor, depth: 1}
	if f.srv.callerHasTaskBudget(launcher) {
		t.Fatal("the launch caller has a task budget")
	}
	if coordinatorAllowed(launcher, false) != nil {
		t.Fatal("coordinator gate treats the launch caller as a coordinator")
	}
	if f.srv.isWorkbenchSession(launcher) {
		t.Fatal("the launch caller is a workbench session")
	}
	req := apiv1.CreateSessionRequest{Repo: "dev-env", Agent: "codex", Mode: "remote", Model: "gpt-6-astra"}
	if _, err := f.srv.newSession(context.Background(), req, launcher); err == nil {
		t.Fatal("the launch caller started a codex remote session")
	}
	sess, err := f.srv.newSession(context.Background(), apiv1.CreateSessionRequest{Repo: "dev-env", Agent: "claude", Mode: "local", Model: "claude-opus-5-5"}, launcher)
	if err != nil || sess.Spec.Profile != "" || sess.Spec.Parent != "portal/"+launchActor {
		t.Fatalf("%+v %v", sess, err)
	}
	if got := len(validation.IsDNS1123Label("portal/" + launchActor)); got == 0 {
		t.Fatal("the launch parent is a valid session name")
	}
}

// HTML characters in a prompt are not escaped into the create body. A prompt of
// 20,000 "<" is 20 KB in the request and, escaped for agentd's session document
// (which the existing create path always does), about 120 KB: inside both
// bounds. A prompt of 50,000 "<" is inside the 64 KiB prompt limit but its
// session document would exceed agentd's 128 KiB transport, so the existing
// check refuses it with 422, not with a body-cap 413.
func TestPortalLaunchPromptWithHTMLCharacters(t *testing.T) {
	f := launchFixture(t, "dev-env")
	req := launchTask()
	req.Prompt = strings.Repeat("<", 20000)
	w := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", req)
	if w.Code != http.StatusCreated {
		t.Fatalf("%d %.200s", w.Code, w.Body.String())
	}
	if got := sessionsIn(t, f)[0].Spec.Prompt; got != req.Prompt {
		t.Fatalf("prompt changed: %d bytes", len(got))
	}
	big := launchTask()
	big.RequestID = "request-0000000002"
	big.Prompt = strings.Repeat("<", 50000)
	w = f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", big)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "bounded transport") || len(sessionsIn(t, f)) != 1 {
		t.Fatalf("%d %.200s", w.Code, w.Body.String())
	}
}

// Every launch runs on the profile the operator names. A request cannot choose
// one, a missing profile turns the route off, and a name the templates lack is
// refused before anything is created.
func TestPortalLaunchProfileIsPinned(t *testing.T) {
	f := launchFixture(t, "dev-env")
	f.srv.PortalLaunchProfile = "ops"
	if w := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", launchTask()); w.Code != http.StatusCreated || sessionsIn(t, f)[0].Spec.Profile != "ops" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", `{"requestID":"`+launchRequest+`x","actorRef":"`+launchActor+`","repo":"dev-env","mode":"remote","model":"claude-opus-5-5","profile":"full"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("a profile field was accepted: %d", w.Code)
	}

	f = launchFixture(t, "dev-env")
	f.srv.PortalLaunchProfile = "no-such-profile"
	wantError(t, f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", launchTask()), http.StatusForbidden, apiv1.CodeForbidden)
	f.srv.PortalLaunchProfile = ""
	wantError(t, f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", launchTask()), http.StatusForbidden, apiv1.CodeForbidden)
	if n := len(sessionsIn(t, f)); n != 0 {
		t.Fatalf("%d sessions created", n)
	}
}

// At most the cap of portal launches are unfinished at once, across actors. A
// repeat of an existing request still returns its session at the cap, and a
// finished session no longer counts.
func TestPortalLaunchIsCappedWhileUnfinished(t *testing.T) {
	f := launchFixture(t, "dev-env")
	f.srv.PortalLaunchMaxUnfinished = 0 // the default, 2
	launch := func(id, actor string) *httptest.ResponseRecorder {
		req := launchTask()
		req.RequestID, req.ActorRef = id, actor
		return f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", req)
	}
	// The default cap is 2.
	if w := launch("request-aaaaaaaaaaaa", launchActor); w.Code != http.StatusCreated {
		t.Fatalf("first %d %s", w.Code, w.Body.String())
	}
	second := launch("request-bbbbbbbbbbbb", "actor-000000000002")
	if second.Code != http.StatusCreated {
		t.Fatalf("second %d %s", second.Code, second.Body.String())
	}
	wantError(t, launch("request-cccccccccccc", "actor-000000000003"), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	if n := len(sessionsIn(t, f)); n != 2 {
		t.Fatalf("%d sessions: the refused launch created one", n)
	}
	// A repeat of an existing request returns its session at the cap.
	again := launch("request-bbbbbbbbbbbb", "actor-000000000002")
	if again.Code != http.StatusOK || decode[apiv1.PortalLaunchReceipt](t, again).SessionName != decode[apiv1.PortalLaunchReceipt](t, second).SessionName || len(sessionsIn(t, f)) != 2 {
		t.Fatalf("repeat at the cap: %d %s", again.Code, again.Body.String())
	}
	// Sessions that are not portal launches do not count toward it.
	other := v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "someone-elses", Namespace: sessionNS}, Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Parent: humanSA}}
	if err := f.c.Create(context.Background(), &other); err != nil {
		t.Fatal(err)
	}
	wantError(t, launch("request-cccccccccccc", "actor-000000000003"), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	// A finished (here suspended) launch stops counting, so one more fits.
	var portalNames []string
	for _, s := range sessionsIn(t, f) {
		if strings.HasPrefix(s.Spec.Parent, "portal/") {
			portalNames = append(portalNames, s.Name)
		}
	}
	var done v1alpha1.AgentSession
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: portalNames[0]}, &done); err != nil {
		t.Fatal(err)
	}
	done.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if err := f.c.Update(context.Background(), &done); err != nil {
		t.Fatal(err)
	}
	if w := launch("request-cccccccccccc", "actor-000000000003"); w.Code != http.StatusCreated {
		t.Fatalf("after one finished: %d %s", w.Code, w.Body.String())
	}
	wantError(t, launch("request-dddddddddddd", "actor-000000000004"), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	// The cap is configurable: raised to 3 it admits another, and a cap of 1 refuses at one.
	f.srv.PortalLaunchMaxUnfinished = 3
	if w := launch("request-dddddddddddd", "actor-000000000004"); w.Code != http.StatusCreated {
		t.Fatalf("cap 3: %d %s", w.Code, w.Body.String())
	}
	f.srv.PortalLaunchMaxUnfinished = 1
	wantError(t, launch("request-eeeeeeeeeeee", "actor-000000000005"), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
}

// The launch profile is resolved on every request: a profile that names any
// ServiceAccount (such as the workbench's, under another name) or that the
// templates no longer hold is refused before anything is created.
func TestPortalLaunchRefusesAProfileWithAServiceAccount(t *testing.T) {
	f := launchFixture(t, "dev-env")
	_, dev, err := f.tmpl.Profile("dev")
	if err != nil {
		t.Fatal(err)
	}
	dev.ServiceAccount = templates.WorkbenchServiceAccount
	f.tmpl.Profiles["portal-tools"] = dev
	for _, profile := range []string{"portal-tools", "no-such-profile"} {
		f.srv.PortalLaunchProfile = profile
		w := f.do(http.MethodPost, apiv1.PortalLaunchPath, "tok-portal", launchTask())
		if w.Code != http.StatusForbidden || len(sessionsIn(t, f)) != 0 {
			t.Fatalf("%s: status %d, %d sessions", profile, w.Code, len(sessionsIn(t, f)))
		}
	}
}
