package controller

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/templates"
)

func TestTmpDefaultPreservesPodBytes(t *testing.T) {
	// Pin the whole default pod's JSON bytes on purpose, including its template
	// revision label. Regenerate deliberately only when pod.go changes for
	// another reason.
	const legacySHA256 = "c91e28583563011e5e83da0056e2965be4d79bc44212d4dafd2d494029e75e1c"
	pod, err := buildPod(taskSession(), exampleTemplates(t), "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(pod)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(b)); got != legacySHA256 {
		t.Fatalf("omitting tmpSizeLimit changed pod bytes: SHA256=%s, want %s", got, legacySHA256)
	}
}

func TestPodTmpSizeByClass(t *testing.T) {
	b, err := os.ReadFile("../templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		doc := string(b)
		if explicit {
			for class, limit := range map[string]string{"S": "12Gi", "M": "16Gi", "L": "32Gi"} {
				doc = strings.Replace(doc, "  "+class+":\n", "  "+class+":\n    tmpSizeLimit: "+limit+"\n", 1)
			}
		}
		tmpl, err := templates.Parse(map[string]string{templates.Key: doc})
		if err != nil {
			t.Fatal(err)
		}
		for _, class := range []v1alpha1.SizeClass{v1alpha1.SizeS, v1alpha1.SizeM, v1alpha1.SizeL, ""} {
			for _, hold := range []bool{false, true} {
				t.Run(fmt.Sprintf("explicit=%v/class=%s/hold=%v", explicit, class, hold), func(t *testing.T) {
					s := taskSession()
					s.Spec.Size = class
					pod, err := buildSessionPod(s, tmpl, "", hold, false, false, false)
					if err != nil {
						t.Fatal(err)
					}
					want := "8Gi"
					if explicit {
						switch {
						case hold || class == v1alpha1.SizeS:
							want = "12Gi"
						case class == v1alpha1.SizeL:
							want = "32Gi"
						default:
							want = "16Gi"
						}
					}
					for _, v := range pod.Spec.Volumes {
						if v.Name == "tmp" && v.EmptyDir != nil && v.EmptyDir.SizeLimit != nil && v.EmptyDir.SizeLimit.String() == want {
							return
						}
					}
					t.Fatalf("no /tmp EmptyDir with sizeLimit %s", want)
				})
			}
		}
	}
}

func TestCreatesPodWithConfiguredTmpLimit(t *testing.T) {
	doc := strings.Replace(exampleDoc, "  M:\n", "  M:\n    tmpSizeLimit: 16Gi\n", 1)
	setTemplates(t, doc)
	op := startOperator(t)
	s := newSession(t, nil)
	pod := waitPod(t, s.Name)
	for _, v := range pod.Spec.Volumes {
		if v.Name == "tmp" && v.EmptyDir != nil && v.EmptyDir.SizeLimit != nil && v.EmptyDir.SizeLimit.String() == "16Gi" {
			assertNoPodOrVolumeWrites(t, op)
			return
		}
	}
	t.Fatal("API-server pod did not retain the configured /tmp limit")
}
