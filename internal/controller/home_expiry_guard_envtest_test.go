package controller

// T9's admission part (DESIGN-025 section 9): dev-env-home-expiry-guard, the
// reference copy in testdata/haynes-ops, refuses every ServiceAccount a
// request that adds, changes or removes the owner's override, and admits a
// user's. The ServiceAccounts get RBAC to write AgentSessions here, so a
// refusal comes from the policy, not from RBAC.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const homeExpiryGuardName = "dev-env-home-expiry-guard"

func TestHomeExpiryOverrideGuardIsOwnerOnly(t *testing.T) {
	ctx := context.Background()
	data, err := os.ReadFile(filepath.Join("testdata", "haynes-ops", "home-expiry-guard.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var u unstructured.Unstructured
		if err := dec.Decode(&u.Object); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		if len(u.Object) == 0 {
			continue
		}
		if err := k8s.Create(ctx, &u); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = k8s.Delete(context.Background(), &u) })
	}

	accounts := []struct{ ns, name string }{{sessionNS, ServiceAccountName}, {sessionNS, "grant-h5test"}, {systemNS, "dev-env-operator"}}
	role := &rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "test-h5-agentsession-writer"},
		Rules: []rbacv1.PolicyRule{{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"agentsessions"}, Verbs: []string{"get", "create", "update", "patch"}}}}
	binding := &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "test-h5-agentsession-writer"},
		RoleRef:  rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role.Name},
		Subjects: []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: "tom"}}}
	for _, a := range accounts {
		binding.Subjects = append(binding.Subjects, rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: a.ns, Name: a.name})
	}
	for _, o := range []client.Object{role, binding} {
		if err := k8s.Create(ctx, o); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = k8s.Delete(context.Background(), o) })
	}
	as := func(user string, groups ...string) client.Client {
		cfg := rest.CopyConfig(restCfg)
		cfg.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: append(groups, "system:authenticated")}
		c, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	serviceAccount := func(ns, name string) client.Client {
		return as("system:serviceaccount:"+ns+":"+name, "system:serviceaccounts", "system:serviceaccounts:"+ns)
	}
	byPolicy := func(err error) bool {
		return apierrors.IsForbidden(err) && strings.Contains(err.Error(), homeExpiryGuardName)
	}
	seq := 0
	session := func(t *testing.T, annotation string) *v1alpha1.AgentSession {
		t.Helper()
		seq++
		s := taskSession()
		s.Name, s.Namespace, s.UID, s.ResourceVersion = "h5-guard-"+strings.ToLower(time.Now().Format("150405"))+"-"+string(rune('a'+seq)), sessionNS, "", ""
		if annotation != "" {
			s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: annotation}
		}
		if err := k8s.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			purgeEnv(&v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace}})
		})
		return s
	}
	// The API server loads a new policy a moment after it is created.
	operator := serviceAccount(systemNS, "dev-env-operator")
	probe := session(t, "")
	deadline := time.Now().Add(wait)
	for {
		var s v1alpha1.AgentSession
		if err := operator.Get(ctx, client.ObjectKeyFromObject(probe), &s); err != nil {
			t.Fatal(err)
		}
		s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryReleaseNow}
		err := operator.Update(ctx, &s)
		if byPolicy(err) {
			break
		}
		if err == nil {
			// Not enforced yet: an admin takes the override back off the
			// probe, and the probe runs again.
			var undo v1alpha1.AgentSession
			if err := k8s.Get(ctx, client.ObjectKeyFromObject(probe), &undo); err != nil {
				t.Fatal(err)
			}
			delete(undo.Annotations, v1alpha1.AnnotationHomeExpiry)
			if err := k8s.Update(ctx, &undo); err != nil {
				t.Fatal(err)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was not enforced within %s: %v", homeExpiryGuardName, wait, err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	edit := func(c client.Client, s *v1alpha1.AgentSession, change func(map[string]string) map[string]string) error {
		var got v1alpha1.AgentSession
		if err := c.Get(ctx, client.ObjectKeyFromObject(s), &got); err != nil {
			return err
		}
		got.Annotations = change(got.Annotations)
		return c.Update(ctx, &got)
	}
	set := func(v string) func(map[string]string) map[string]string {
		return func(m map[string]string) map[string]string {
			if m == nil {
				m = map[string]string{}
			}
			m[v1alpha1.AnnotationHomeExpiry] = v
			return m
		}
	}
	remove := func(m map[string]string) map[string]string { delete(m, v1alpha1.AnnotationHomeExpiry); return m }
	other := func(m map[string]string) map[string]string {
		if m == nil {
			m = map[string]string{}
		}
		m["test.dev-env/unrelated"] = "x"
		return m
	}

	for _, a := range accounts {
		t.Run(a.ns+"/"+a.name, func(t *testing.T) {
			c := serviceAccount(a.ns, a.name)
			created := taskSession()
			created.Name, created.Namespace, created.UID, created.ResourceVersion = "h5-guard-create-"+strings.ReplaceAll(a.name, "-", ""), sessionNS, "", ""
			created.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
			if err := c.Create(ctx, created); !byPolicy(err) {
				t.Errorf("a create with the override: %v", err)
				purgeEnv(created)
			}
			plain, held := session(t, ""), session(t, v1alpha1.HomeExpiryHold)
			for name, err := range map[string]error{
				"add hold":             edit(c, plain, set(v1alpha1.HomeExpiryHold)),
				"add release-now":      edit(c, plain, set(v1alpha1.HomeExpiryReleaseNow)),
				"change to release":    edit(c, held, set(v1alpha1.HomeExpiryReleaseNow)),
				"remove the hold":      edit(c, held, remove),
				"add an invalid value": edit(c, plain, set("Hold")),
			} {
				if !byPolicy(err) {
					t.Errorf("%s: %v; want refused by %s", name, err, homeExpiryGuardName)
				}
			}
			// Writes that leave the override as it is are not this policy's.
			if err := edit(c, held, other); err != nil {
				t.Errorf("an unrelated change beside a hold refused: %v", err)
			}
			if err := edit(c, plain, other); err != nil {
				t.Errorf("an unrelated change refused: %v", err)
			}
			for _, s := range []*v1alpha1.AgentSession{plain, held} {
				var got v1alpha1.AgentSession
				if err := k8s.Get(ctx, client.ObjectKeyFromObject(s), &got); err != nil {
					t.Fatal(err)
				}
				if got.Annotations[v1alpha1.AnnotationHomeExpiry] != s.Annotations[v1alpha1.AnnotationHomeExpiry] {
					t.Errorf("the override changed: %q", got.Annotations[v1alpha1.AnnotationHomeExpiry])
				}
			}
		})
	}

	t.Run("a user", func(t *testing.T) {
		tom := as("tom")
		s := session(t, "")
		for name, change := range map[string]func(map[string]string) map[string]string{
			"hold": set(v1alpha1.HomeExpiryHold), "release-now": set(v1alpha1.HomeExpiryReleaseNow), "remove": remove,
		} {
			if err := edit(tom, s, change); err != nil {
				t.Errorf("%s by a user refused: %v", name, err)
			}
		}
	})

}
