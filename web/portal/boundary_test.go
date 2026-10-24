package portal

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

type authFunc func(*http.Request) (Principal, error)

func (f authFunc) Authenticate(r *http.Request) (Principal, error) { return f(r) }

type authorizeFunc func(context.Context, Principal, Resource) error

func (f authorizeFunc) Authorize(c context.Context, p Principal, r Resource) error { return f(c, p, r) }

type readFunc func(context.Context, Resource) (Snapshot, error)

func (f readFunc) Read(c context.Context, r Resource) (Snapshot, error) { return f(c, r) }

var fixtureNow = time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)
var fixturePrincipal = Principal{"https://identity.example/issuer/", "subject-one"}
var fixtureScope = Resource{"project-one", "internal", ""}

func sample(scope Resource) Snapshot {
	r := scope
	if r.Task == "" {
		r.Task = "task-one"
	}
	return Snapshot{Resource: scope, State: Verified, UpdatedAt: fixtureNow.Add(-2 * time.Minute), VerifiedAt: fixtureNow.Add(-time.Minute), Items: []Item{{
		Resource: r, WorkPackage: 178, Group: HandoffMissing, Title: "Synthetic task", Owner: "Outcome owner", NextActor: "Outcome owner", NextAction: "Collect durable result",
		WorkerState: "Exited", TaskOutcome: "Unverified", UpdatedAt: fixtureNow.Add(-2 * time.Minute), VerifiedAt: fixtureNow.Add(-time.Minute),
	}}}
}

func dependencies() Dependencies {
	return Dependencies{
		Authenticator: authFunc(func(*http.Request) (Principal, error) { return fixturePrincipal, nil }),
		Authorizer: authorizeFunc(func(_ context.Context, p Principal, r Resource) error {
			if p != fixturePrincipal || r.Project != fixtureScope.Project || r.Environment != fixtureScope.Environment || (r.Task != "" && r.Task != "task-one") {
				return ErrDenied
			}
			return nil
		}),
		Reader: readFunc(func(_ context.Context, r Resource) (Snapshot, error) { return sample(r), nil }),
		Now:    func() time.Time { return fixtureNow }, FreshFor: 5 * time.Minute,
	}
}

func response(d Dependencies, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, nil)
	NewHandler(d).ServeHTTP(w, r)
	return w
}

const attentionPath = "/portal/projects/project-one/environments/internal/attention"
const taskPath = "/portal/projects/project-one/environments/internal/tasks/task-one"

func assertRefused(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status || w.Body.String() != "Attention unavailable. Current task state is Unknown.\n" {
		t.Fatalf("unsafe refusal %d %q", w.Code, w.Body.String())
	}
	for name, want := range map[string]string{"Cache-Control": "no-store", "X-Frame-Options": "DENY", "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Content-Type": "text/plain; charset=utf-8"} {
		if w.Header().Get(name) != want {
			t.Fatalf("unsafe refusal header %s", name)
		}
	}
}

func TestTwoUsersTwoProjectsAndEnvironments(t *testing.T) {
	p2 := Principal{fixturePrincipal.Issuer, "subject-two"}
	for _, principal := range []Principal{fixturePrincipal, p2} {
		for _, project := range []string{"project-one", "project-two"} {
			for _, environment := range []string{"internal", "production"} {
				t.Run(principal.Subject+project+environment, func(t *testing.T) {
					d := dependencies()
					reads := 0
					d.Authenticator = authFunc(func(*http.Request) (Principal, error) { return principal, nil })
					d.Authorizer = authorizeFunc(func(_ context.Context, p Principal, r Resource) error {
						allowed := "project-one"
						if p == p2 {
							allowed = "project-two"
						}
						if r.Project != allowed || r.Environment != "internal" {
							return ErrDenied
						}
						return nil
					})
					d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
						reads++
						s := sample(r)
						s.Items[0].Title = "private-" + project
						return s, nil
					})
					w := response(d, http.MethodGet, "/portal/projects/"+project+"/environments/"+environment+"/attention")
					allowed := environment == "internal" && ((principal == fixturePrincipal && project == "project-one") || (principal == p2 && project == "project-two"))
					if allowed {
						if w.Code != 200 || !strings.Contains(w.Body.String(), "private-"+project) || reads != 1 {
							t.Fatal("authorized snapshot missing")
						}
					} else {
						assertRefused(t, w, 404)
						if reads != 0 {
							t.Fatal("read before scope check")
						}
					}
				})
			}
		}
	}
	// The same subject at a different issuer is a different identity.
	d := dependencies()
	d.Authenticator = authFunc(func(*http.Request) (Principal, error) {
		return Principal{"https://other.example/", fixturePrincipal.Subject}, nil
	})
	assertRefused(t, response(d, "GET", attentionPath), 404)
}

func TestGuessedResourcesAndMemberAuthorization(t *testing.T) {
	d := dependencies()
	reads := 0
	d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) { reads++; return sample(r), nil })
	assertRefused(t, response(d, "GET", strings.Replace(taskPath, "task-one", "task-secret", 1)), 404)
	if reads != 0 {
		t.Fatal("guessed resource reached reader")
	}
	// Collection permission is not a wildcard. Reject the entire response if
	// a reader accidentally includes a member outside current permission.
	d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
		s := sample(r)
		s.Items[0].Resource.Task = "task-secret"
		s.Items[0].Title = "SECRET-CANARY"
		return s, nil
	})
	assertRefused(t, response(d, "GET", attentionPath), 404)
}

func TestWithdrawalDuringReadDiscardsAllData(t *testing.T) {
	for _, mode := range []string{"scope", "member", "session", "identity-change", "authorizer-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			d := dependencies()
			read := false
			d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
				read = true
				s := sample(r)
				s.Items[0].Title = "SECRET-CANARY"
				return s, nil
			})
			d.Authenticator = authFunc(func(*http.Request) (Principal, error) {
				if read && mode == "session" {
					return Principal{}, ErrDenied
				}
				if read && mode == "identity-change" {
					return Principal{fixturePrincipal.Issuer, "subject-two"}, nil
				}
				return fixturePrincipal, nil
			})
			d.Authorizer = authorizeFunc(func(_ context.Context, _ Principal, r Resource) error {
				if read {
					if mode == "scope" || (mode == "member" && r.Task != "") {
						return ErrDenied
					}
					if mode == "authorizer-unavailable" {
						return errors.New("SECRET-CANARY backend failure")
					}
				}
				return nil
			})
			status := 404
			if mode == "authorizer-unavailable" {
				status = 503
			}
			assertRefused(t, response(d, "GET", attentionPath), status)
		})
	}
}

func TestMissingDependenciesAndReadStates(t *testing.T) {
	for _, name := range []string{"authn", "authz", "reader", "freshness", "authn-error", "empty-principal", "reader-error", "unavailable-adapter", "unavailable-snapshot", "unknown", "empty"} {
		t.Run(name, func(t *testing.T) {
			d := dependencies()
			switch name {
			case "authn":
				d.Authenticator = nil
			case "authz":
				d.Authorizer = nil
			case "reader":
				d.Reader = nil
			case "freshness":
				d.FreshFor = 0
			case "authn-error":
				d.Authenticator = authFunc(func(*http.Request) (Principal, error) { return Principal{}, errors.New("SECRET-CANARY") })
			case "empty-principal":
				d.Authenticator = authFunc(func(*http.Request) (Principal, error) { return Principal{}, nil })
			case "reader-error":
				d.Reader = readFunc(func(context.Context, Resource) (Snapshot, error) { return Snapshot{}, errors.New("SECRET-CANARY") })
			case "unavailable-adapter":
				d.Reader = UnavailableReader{}
			default:
				d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
					s := sample(r)
					s.Items = nil
					if name == "unknown" {
						s.State = Unknown
					}
					if name == "unavailable-snapshot" {
						s.State = Unavailable
					}
					return s, nil
				})
			}
			w := response(d, "GET", attentionPath)
			if name == "unknown" || name == "empty" {
				want := "Attention unknown"
				if name == "empty" {
					want = "Verified empty"
				}
				if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
					t.Fatal("wrong read truth")
				}
			} else {
				assertRefused(t, w, 503)
			}
		})
	}
	assertRefused(t, response(Dependencies{}, "GET", attentionPath), 503)
}

func TestWrongSourceBindingNeverLeaks(t *testing.T) {
	for _, mode := range []string{"project", "environment", "task", "collection", "duplicate", "group", "head", "oversize", "invalid-utf8", "state", "unknown-with-items", "detail-empty", "canonical-link"} {
		t.Run(mode, func(t *testing.T) {
			d := dependencies()
			path := attentionPath
			if mode == "task" || mode == "detail-empty" {
				path = taskPath
			}
			d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
				s := sample(r)
				s.Items[0].Title = "SECRET-CANARY"
				switch mode {
				case "project":
					s.Items[0].Resource.Project = "project-two"
				case "environment":
					s.Items[0].Resource.Environment = "production"
				case "task":
					s.Items[0].Resource.Task = "task-two"
				case "collection":
					s.Resource.Project = "project-two"
				case "duplicate":
					s.Items = append(s.Items, s.Items[0])
				case "group":
					s.Items[0].Group = "invented"
				case "head":
					s.Items[0].ReviewHead = "javascript:alert(1)"
				case "oversize":
					s.Items[0].Owner = strings.Repeat("x", 513)
				case "invalid-utf8":
					s.Items[0].Owner = string([]byte{0xff})
				case "state":
					s.State = "success-ish"
				case "unknown-with-items":
					s.State = Unknown
				case "detail-empty":
					s.Items = nil
				case "canonical-link":
					s.Items[0].WorkPackage = -1
				}
				return s, nil
			})
			assertRefused(t, response(d, "GET", path), 503)
		})
	}
}

func TestEscapedHostileMetadataAndSeparateTruth(t *testing.T) {
	d := dependencies()
	d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
		s := sample(r)
		i := &s.Items[0]
		i.Title = `<script>alert("x")</script>`
		i.Owner = "\u202e\x00"
		i.NextActor = "\u0301\u3164"
		i.NextAction = `<img src="https://evil.example/" onerror="steal()">`
		i.ReviewState = "Findings returned"
		i.ReviewHead = strings.Repeat("a", 40)
		return s, nil
	})
	w := response(d, "GET", taskPath)
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, "<script>") || strings.Contains(body, "<img ") || strings.Contains(body, "\u202e") || strings.Contains(body, "#ZgotmplZ") {
		t.Fatalf("unsafe HTML %s", body)
	}
	for _, expected := range []string{"&lt;script&gt;", "&lt;img", "<dt>Owner</dt><dd>Unknown</dd>", "<dt>Next actor</dt><dd>Unknown</dd>", "<dt>Task outcome</dt><dd>Unverified</dd>", "<dt>Worker liveness</dt><dd>Exited</dd>", "<dt>Review</dt><dd>Findings returned</dd>", "<dt>Authority</dt><dd>Unknown</dd>", "<dt>Release</dt><dd>Unknown</dd>", `href="https://work.haynesnetwork.com/work_packages/178"`} {
		if !strings.Contains(body, expected) {
			t.Errorf("missing %s", expected)
		}
	}
	if strings.Count(body, "href=") != 1 || strings.Contains(body, "<form") || strings.Contains(body, "<iframe") {
		t.Fatal("active surface or unvalidated link")
	}
	for name, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Content-Security-Policy": contentPolicy} {
		if w.Header().Get(name) != want {
			t.Errorf("missing %s", name)
		}
	}
}

func TestStaleFutureAndUnknownEvidence(t *testing.T) {
	for _, mode := range []string{"fresh", "boundary", "stale", "missing-update", "missing-verified", "future-update", "future-verified", "verification-before-update"} {
		for _, empty := range []bool{false, true} {
			t.Run(mode+map[bool]string{true: "-empty", false: "-row"}[empty], func(t *testing.T) {
				d := dependencies()
				d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
					s := sample(r)
					u, v := s.UpdatedAt, s.VerifiedAt
					switch mode {
					case "boundary":
						u = fixtureNow.Add(-6 * time.Minute)
						v = fixtureNow.Add(-5 * time.Minute)
					case "stale":
						u = fixtureNow.Add(-7 * time.Minute)
						v = fixtureNow.Add(-6 * time.Minute)
					case "missing-update":
						u = time.Time{}
					case "missing-verified":
						v = time.Time{}
					case "future-update":
						u = fixtureNow.Add(time.Minute)
					case "future-verified":
						v = fixtureNow.Add(time.Minute)
					case "verification-before-update":
						v = u.Add(-time.Minute)
					}
					s.UpdatedAt = u
					s.VerifiedAt = v
					s.Items[0].UpdatedAt = u
					s.Items[0].VerifiedAt = v
					if empty {
						s.Items = nil
					}
					return s, nil
				})
				w := response(d, "GET", attentionPath)
				body := w.Body.String()
				if w.Code != 200 {
					t.Fatal(w.Code)
				}
				valid := mode == "fresh" || mode == "boundary"
				if empty && strings.Contains(body, "Verified empty") != valid {
					t.Fatal("empty overstates verification")
				}
				expected := map[string]string{"fresh": "Verified within snapshot window", "boundary": "Verified within snapshot window", "stale": "Stale evidence", "missing-update": "Freshness unknown", "missing-verified": "Freshness unknown", "future-update": "Freshness unknown", "future-verified": "Freshness unknown", "verification-before-update": "Unknown: verification predates source update"}[mode]
				if !empty && !strings.Contains(body, `<p class="freshness">`+expected+`</p>`) {
					t.Fatalf("wrong exact row freshness, want %s", expected)
				}
				if empty && !valid && !strings.Contains(body, `role="status">`+expected+". Listing counts are not verified as current;") {
					t.Fatal("empty listing warning missing")
				}
			})
		}
	}
}

func TestListingFreshnessWithFreshItem(t *testing.T) {
	for _, test := range []struct {
		name              string
		updated, verified time.Time
		warning           string
	}{
		{"fresh", fixtureNow.Add(-2 * time.Minute), fixtureNow.Add(-time.Minute), ""},
		{"boundary", fixtureNow.Add(-6 * time.Minute), fixtureNow.Add(-5 * time.Minute), ""},
		{"missing-update", time.Time{}, fixtureNow.Add(-time.Minute), "Freshness unknown"},
		{"missing-verified", fixtureNow.Add(-2 * time.Minute), time.Time{}, "Freshness unknown"},
		{"stale", fixtureNow.Add(-7 * time.Minute), fixtureNow.Add(-6 * time.Minute), "Stale evidence"},
		{"future-update", fixtureNow.Add(time.Minute), fixtureNow.Add(-time.Minute), "Freshness unknown"},
		{"future-verified", fixtureNow.Add(-time.Minute), fixtureNow.Add(time.Minute), "Freshness unknown"},
		{"verification-before-update", fixtureNow.Add(-time.Minute), fixtureNow.Add(-2 * time.Minute), "Unknown: verification predates source update"},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := dependencies()
			d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
				s := sample(r)
				s.UpdatedAt = test.updated
				s.VerifiedAt = test.verified
				return s, nil
			})
			w := response(d, "GET", attentionPath)
			body := w.Body.String()
			if w.Code != 200 {
				t.Fatal(w.Code)
			}
			if !strings.Contains(body, `<p class="freshness">Verified within snapshot window</p>`) {
				t.Fatal("fresh item proof lost")
			}
			if test.warning != "" {
				if !strings.Contains(body, `role="status">`+test.warning+". Listing counts are not verified as current;") {
					t.Fatal("listing warning missing")
				}
				for _, title := range []string{"Needs you", "Needs worker", "Review returned", "Handoff missing"} {
					if !strings.Contains(body, ">"+title+" · Unknown</h2>") {
						t.Fatalf("unverified count for %s", title)
					}
				}
			} else {
				if strings.Contains(body, "Listing counts are not verified as current") || !strings.Contains(body, ">Needs you · 0</h2>") || !strings.Contains(body, ">Handoff missing · 1</h2>") {
					t.Fatal("fresh listing count incorrect")
				}
			}
		})
	}
}

func TestCancellationDuringReadReturnsNoMetadata(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := dependencies()
	d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
		s := sample(r)
		s.Items[0].Title = "SECRET-CANARY"
		cancel()
		return s, nil
	})
	r := httptest.NewRequest("GET", attentionPath, nil).WithContext(ctx)
	w := httptest.NewRecorder()
	NewHandler(d).ServeHTTP(w, r)
	assertRefused(t, w, 503)
}

func TestExactRowAndByteBounds(t *testing.T) {
	for _, count := range []int{24, 25} {
		t.Run(fmt.Sprintf("rows-%d", count), func(t *testing.T) {
			d := dependencies()
			s := sample(fixtureScope)
			item := s.Items[0]
			s.Items = nil
			allowed := map[Resource]bool{fixtureScope: true}
			for index := range count {
				row := item
				row.Resource.Task = fmt.Sprintf("task-%d", index)
				s.Items = append(s.Items, row)
				allowed[row.Resource] = true
			}
			d.Authorizer = authorizeFunc(func(_ context.Context, p Principal, r Resource) error {
				if p != fixturePrincipal || !allowed[r] {
					return ErrDenied
				}
				return nil
			})
			d.Reader = readFunc(func(context.Context, Resource) (Snapshot, error) { return s, nil })
			w := response(d, "GET", attentionPath)
			if count == 24 {
				if w.Code != 200 || strings.Count(w.Body.String(), "<article ") != 24 {
					t.Fatal("valid boundary rejected")
				}
			} else {
				assertRefused(t, w, 503)
			}
		})
	}
	for _, length := range []int{512, 513} {
		t.Run(fmt.Sprintf("bytes-%d", length), func(t *testing.T) {
			d := dependencies()
			owner := strings.Repeat("é", 256)
			if length == 513 {
				owner += "x"
			}
			d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
				s := sample(r)
				s.Items[0].Owner = owner
				return s, nil
			})
			w := response(d, "GET", attentionPath)
			if length == 512 {
				if w.Code != 200 || !strings.Contains(w.Body.String(), "<dt>Owner</dt><dd>"+owner+"</dd>") {
					t.Fatal("512-byte Unicode text rejected")
				}
			} else {
				assertRefused(t, w, 503)
			}
		})
	}
}

func TestRenderedStyleMatchesCSP(t *testing.T) {
	w := response(dependencies(), "GET", attentionPath)
	body := w.Body.String()
	_, styleAndRest, ok := strings.Cut(body, "<style>")
	if !ok {
		t.Fatal("missing style")
	}
	style, _, ok := strings.Cut(styleAndRest, "</style>")
	if !ok {
		t.Fatal("missing style end")
	}
	digest := sha256.Sum256([]byte(style))
	hash := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), hash) {
		t.Fatal("rendered CSS differs from allowed CSP hash")
	}
	if !strings.Contains(body, `aria-label="Open canonical record for Synthetic task (task-one)"`) {
		t.Fatal("accessible name lacks visible link label")
	}
}

func TestFutureVerificationCannotAppearAsTrustedTimestamp(t *testing.T) {
	d := dependencies()
	future := fixtureNow.Add(time.Hour)
	d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
		s := sample(r)
		s.VerifiedAt = future
		s.Items[0].VerifiedAt = future
		return s, nil
	})
	w := response(d, "GET", attentionPath)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Last verified: Unknown (untrusted future timestamp:") || !strings.Contains(w.Body.String(), "<dt>Last verified</dt><dd>Unknown (untrusted future timestamp:") {
		t.Fatal("future evidence was presented as trusted verification")
	}
}

func TestMethodsRoutesAndSpoofedHeaders(t *testing.T) {
	d := dependencies()
	calls := 0
	d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) { calls++; return sample(r), nil })
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE"} {
		w := response(d, method, attentionPath)
		assertRefused(t, w, 405)
		if w.Header().Get("Allow") != "GET" {
			t.Fatal("missing Allow")
		}
	}
	for _, path := range []string{"/", "/v1/sessions", "/portal/projects/project-one/environments/internal/attention?url=http://169.254.169.254/", taskPath + "/", strings.Replace(attentionPath, "project-one", "..", 1), strings.Replace(taskPath, "task-one", "task%2fone", 1)} {
		assertRefused(t, response(d, "GET", path), 404)
	}
	if calls != 0 {
		t.Fatal("rejected route reached reader")
	}
	d = dependencies()
	d.Authenticator = authFunc(func(*http.Request) (Principal, error) { return Principal{}, ErrDenied })
	r := httptest.NewRequest("GET", attentionPath, nil)
	for _, key := range []string{"X-Forwarded-User", "X-Forwarded-Sub", "X-Auth-Request-User", "Authorization", "Forwarded"} {
		r.Header.Set(key, "subject-one")
	}
	w := httptest.NewRecorder()
	NewHandler(d).ServeHTTP(w, r)
	assertRefused(t, w, 404)
	// Headers also cannot replace a successfully authenticated principal with a
	// different subject. Current authorization sees only the trusted pair.
	d = dependencies()
	original := d.Authorizer
	d.Authorizer = authorizeFunc(func(c context.Context, p Principal, resource Resource) error {
		if p != fixturePrincipal {
			t.Error("header replaced trusted identity")
		}
		return original.Authorize(c, p, resource)
	})
	r = httptest.NewRequest("GET", attentionPath, nil)
	r.Header.Set("X-Forwarded-User", "subject-two")
	r.Header.Set("X-Forwarded-Sub", "subject-two")
	r.Header.Set("X-Forwarded-Issuer", "https://other.example/")
	w = httptest.NewRecorder()
	NewHandler(d).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("spoofed header changed authorized principal")
	}
}

func TestProjectWorkerBindingAndPayloadExclusion(t *testing.T) {
	r := Resource{"project-one", "internal", "task-one"}
	b := SourceBinding{Resource: r, ClaimUID: "claim-one", SessionName: "worker-one", SessionUID: "uid-one"}
	claim := apiv1.TaskClaim{UID: "claim-one", Identity: apiv1.TaskClaimIdentity{Project: r.Project, TaskKey: r.Task}, Owner: apiv1.TaskClaimOwner{Name: "worker-one", UID: "uid-one"}}
	session := apiv1.Session{Name: "worker-one", UID: "uid-one", Phase: "Suspended", Prompt: "SECRET-PROMPT", Repo: "SECRET-REPOSITORY", Model: "SECRET-MODEL", Outcome: &apiv1.Outcome{}}
	got, err := ProjectWorker(b, claim, session)
	if err != nil || got.Owner != "worker-one" || got.WorkerPhase != "Suspended" {
		t.Fatalf("projection %v %v", got, err)
	}
	for _, mode := range []string{"project", "task", "claim-uid", "owner-name", "owner-uid", "session-name", "session-uid", "empty-binding", "archived"} {
		t.Run(mode, func(t *testing.T) {
			c, s, binding := claim, session, b
			switch mode {
			case "archived":
				c.Archived = true
			case "project":
				c.Identity.Project = "project-two"
			case "task":
				c.Identity.TaskKey = "task-two"
			case "claim-uid":
				c.UID = "replacement"
			case "owner-name":
				c.Owner.Name = "worker-two"
			case "owner-uid":
				c.Owner.UID = "replacement"
			case "session-name":
				s.Name = "worker-two"
			case "session-uid":
				s.UID = "replacement"
			case "empty-binding":
				binding.SessionUID = ""
			}
			out, err := ProjectWorker(binding, c, s)
			if !errors.Is(err, ErrUnavailable) || out != (WorkerSummary{}) {
				t.Fatal("foreign source escaped")
			}
		})
	}
}

func TestEssentialFactsStayOutsideSeparateEvidence(t *testing.T) {
	w := response(dependencies(), "GET", attentionPath)
	body := w.Body.String()
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	details := strings.Index(body, "<details")
	if details < 0 {
		t.Fatal("separate evidence disclosure missing")
	}
	for _, label := range []string{"<dt>Next action</dt>", "<dt>Owner</dt>", "<dt>Next actor</dt>", "<dt>Canonical source</dt>", "<dt>Source age</dt>", "<dt>Source updated</dt>", "<dt>Last verified</dt>", "<dt>Task outcome</dt>", "<dt>Worker liveness</dt>"} {
		if i := strings.Index(body, label); i < 0 || i > details {
			t.Fatalf("%s not visible before disclosure", label)
		}
	}
	secondary := body[details:]
	for _, label := range []string{"<dt>Review</dt>", "<dt>Reviewed head</dt>", "<dt>Authority</dt>", "<dt>Release</dt>"} {
		if !strings.Contains(secondary, label) {
			t.Fatalf("%s left the separate evidence", label)
		}
	}
	if strings.Contains(secondary, "<dt>Task outcome</dt>") || strings.Contains(secondary, "<dt>Worker liveness</dt>") {
		t.Fatal("handoff evidence was only inside the disclosure")
	}
	if !strings.Contains(body, "<summary>Separate review, authority and release</summary>") {
		t.Fatal("disclosure name missing")
	}
	if !strings.Contains(body, `aria-describedby="fresh-task-one"`) || !strings.Contains(body, `<p class="freshness">Verified within snapshot window</p>`) {
		t.Fatal("freshness is not tied to the row")
	}
	if strings.Count(body, "href=") != 1 || strings.Contains(body, "<script") || strings.Contains(body, "<form") {
		t.Fatal("presentation added an active control")
	}
}

func TestEmptyAndUnknownListingsDoNotInventCurrentZeros(t *testing.T) {
	d := dependencies()
	d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
		s := sample(r)
		s.Items = nil
		return s, nil
	})
	body := response(d, "GET", attentionPath).Body.String()
	for _, title := range []string{"Needs you", "Needs worker", "Review returned", "Handoff missing"} {
		if !strings.Contains(body, ">"+title+" · 0</h2>") || !strings.Contains(body, "No items in this group.") {
			t.Fatalf("verified empty group missing: %s", title)
		}
	}
	if !strings.Contains(body, "Verified empty") {
		t.Fatal("verified empty notice missing")
	}

	d = dependencies()
	d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
		s := sample(r)
		s.Items = nil
		s.State = Unknown
		return s, nil
	})
	body = response(d, "GET", attentionPath).Body.String()
	if !strings.Contains(body, "Attention unknown") || strings.Contains(body, "· 0") || strings.Contains(body, "No items in this group.") {
		t.Fatal("unknown read presented a current count")
	}

	d = dependencies()
	d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
		s := sample(r)
		s.Items = nil
		s.UpdatedAt = fixtureNow.Add(-7 * time.Minute)
		s.VerifiedAt = fixtureNow.Add(-6 * time.Minute)
		return s, nil
	})
	body = response(d, "GET", attentionPath).Body.String()
	if strings.Contains(body, "· 0") || strings.Contains(body, "Verified empty") || !strings.Contains(body, ">Needs you · Unknown</h2>") || !strings.Contains(body, "Count for this group is Unknown.") {
		t.Fatal("stale empty listing presented a current zero")
	}
}

func TestTaskRouteDoesNotClaimOtherGroupsEmpty(t *testing.T) {
	w := response(dependencies(), "GET", taskPath)
	body := w.Body.String()
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if strings.Contains(body, "· 0") || strings.Contains(body, "No items in this group.") || strings.Contains(body, "Verified empty") {
		t.Fatal("task page implied empty groups beyond its scope")
	}
	if !strings.Contains(body, ">Handoff missing · 1</h2>") || !strings.Contains(body, "Other Attention groups are outside this page and are not verified empty.") {
		t.Fatal("task scope wording missing")
	}
	for _, title := range []string{"Needs you", "Needs worker", "Review returned"} {
		if strings.Contains(body, ">"+title+" ·") {
			t.Fatalf("task page rendered %s", title)
		}
	}
}

func TestReviewReturnedShowsHeadBeforeDisclosure(t *testing.T) {
	d := dependencies()
	d.Reader = readFunc(func(_ context.Context, r Resource) (Snapshot, error) {
		s := sample(r)
		s.Items[0].Group = ReviewReturned
		s.Items[0].ReviewState = "Findings returned"
		s.Items[0].ReviewHead = strings.Repeat("a", 40)
		return s, nil
	})
	body := response(d, "GET", attentionPath).Body.String()
	details := strings.Index(body, "<details")
	review := strings.Index(body, "<dt>Review</dt>")
	head := strings.Index(body, "<dt>Reviewed head</dt>")
	if details < 0 || review < 0 || head < 0 || review > details || head > details {
		t.Fatal("review evidence hidden in the disclosure")
	}
	if strings.Contains(body[details:], "<dt>Review</dt>") || strings.Contains(body[details:], "<dt>Reviewed head</dt>") {
		t.Fatal("review evidence duplicated inside the disclosure")
	}
	if !strings.Contains(body, "<summary>Separate outcome, worker, authority and release</summary>") {
		t.Fatal("review disclosure name missing")
	}
}
