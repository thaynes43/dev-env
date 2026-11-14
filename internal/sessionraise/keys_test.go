package sessionraise

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type memStore struct {
	mu      sync.Mutex
	objects map[string]Object
	writes  map[string]int
	// afterWrite, when set, is returned by the next Get of that name
	// instead of what Update stored. It proves a signer uses the read-back.
	afterWrite map[string]Object
}

func newMem() *memStore {
	return &memStore{objects: map[string]Object{}, writes: map[string]int{}}
}

func (m *memStore) Get(_ context.Context, name string) (Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[name]
	if !ok {
		return Object{}, ErrUnreadable
	}
	if alt, ok := m.afterWrite[name]; ok && m.writes[name] > 0 {
		return cloneObj(alt), nil
	}
	return cloneObj(obj), nil
}

func (m *memStore) Update(_ context.Context, name string, data map[string][]byte, rv string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[name]
	if !ok {
		return ErrUnreadable
	}
	if obj.RV != rv {
		return ErrConflict
	}
	obj.Data = cloneData(data)
	obj.RV = rv + "x"
	m.objects[name] = obj
	m.writes[name]++
	return nil
}

func cloneObj(in Object) Object {
	return Object{Data: cloneData(in.Data), RV: in.RV}
}

func cloneData(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

func emptyObj() Object { return Object{Data: map[string][]byte{}, RV: "1"} }

func TestTwoReplicasLeaveOneKey(t *testing.T) {
	st := newMem()
	st.objects[OperatorSecretName] = emptyObj()
	st.objects[OperatorPublicName] = emptyObj()
	var wg sync.WaitGroup
	got := make([]ed25519.PrivateKey, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = Ensure(context.Background(), st, OperatorSecretName, OperatorPublicName, true)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("replica %d: %v", i, err)
		}
	}
	if st.writes[OperatorSecretName] != 1 {
		t.Fatalf("secret writes %d", st.writes[OperatorSecretName])
	}
	if !got[0].Equal(got[1]) {
		t.Fatal("replicas returned different keys")
	}
	stored := st.objects[OperatorSecretName].Data[SecretKey]
	if !ed25519.PrivateKey(stored).Equal(got[0]) {
		t.Fatal("returned key is not the stored key")
	}
}

func TestReplicaSignsOnlyTheKeyItReadBack(t *testing.T) {
	_, generated, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	readBack, readPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	st := newMem()
	st.objects[OperatorSecretName] = emptyObj()
	st.objects[OperatorPublicName] = emptyObj()
	st.afterWrite = map[string]Object{
		OperatorSecretName: {Data: map[string][]byte{SecretKey: readPriv}, RV: "read"},
	}
	got, err := Ensure(context.Background(), st, OperatorSecretName, OperatorPublicName, true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(readPriv) || got.Equal(generated) {
		t.Fatal("Ensure returned a key other than the read-back key")
	}
	rec := Record{SessionUID: "uid", State: StatePending, Owner: Owner{Words: "yes", WordsHash: WordsHash("yes")}}
	signed, err := Sign(rec, got)
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(signed, readBack) {
		t.Fatal("signature does not verify with the read-back public key")
	}
}

func TestLeaderPublishesAndDoesNotOverwrite(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	st := newMem()
	st.objects[OperatorSecretName] = Object{Data: map[string][]byte{SecretKey: priv}, RV: "1"}
	st.objects[OperatorPublicName] = emptyObj()
	if _, err := Ensure(context.Background(), st, OperatorSecretName, OperatorPublicName, true); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), st, OperatorSecretName, OperatorPublicName); err != nil {
		t.Fatal(err)
	}
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	st.objects[OperatorPublicName] = Object{Data: map[string][]byte{PublicKeyField: []byte(encodePublic(other))}, RV: "9"}
	writes := st.writes[OperatorPublicName]
	_, err = Ensure(context.Background(), st, OperatorSecretName, OperatorPublicName, true)
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("overwrite: %v", err)
	}
	if st.writes[OperatorPublicName] != writes {
		t.Fatal("a different published key was overwritten")
	}
	if _, err := Load(context.Background(), st, OperatorSecretName, OperatorPublicName); !errors.Is(err, ErrMismatch) {
		t.Fatalf("load: %v", err)
	}
	_ = pub
}

func encodePublic(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

func TestKeyMustEqualItsSeed(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	st := newMem()
	bad := append([]byte(nil), priv...)
	bad[ed25519.SeedSize] ^= 0xff
	st.objects[OperatorSecretName] = Object{Data: map[string][]byte{SecretKey: bad}, RV: "1"}
	if _, err := readKey(context.Background(), st, OperatorSecretName); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("mismatched key: %v", err)
	}
	short := Object{Data: map[string][]byte{SecretKey: priv[:8]}, RV: "1"}
	st.objects[OperatorSecretName] = short
	if _, err := readKey(context.Background(), st, OperatorSecretName); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("short key: %v", err)
	}
	st.objects[OperatorSecretName] = Object{Data: map[string][]byte{}, RV: "1"}
	if _, err := readKey(context.Background(), st, OperatorSecretName); !errors.Is(err, ErrNoKey) {
		t.Fatalf("empty key: %v", err)
	}
}

func TestTwoReplicasLeaveOneKeyInKube(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ObjectNamespace, Name: OperatorSecretName}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: ObjectNamespace, Name: OperatorPublicName}},
	).Build()
	st := Kube{Reader: c, Writer: c, Namespace: ObjectNamespace, Secrets: map[string]bool{OperatorSecretName: true}}
	var wg sync.WaitGroup
	got := make([]ed25519.PrivateKey, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = Ensure(context.Background(), st, OperatorSecretName, OperatorPublicName, true)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("replica %d: %v", i, err)
		}
	}
	if !got[0].Equal(got[1]) {
		t.Fatal("replicas returned different keys")
	}
	first, err := st.Get(context.Background(), OperatorSecretName)
	if err != nil {
		t.Fatal(err)
	}
	other := bytes.Repeat([]byte{7}, ed25519.PrivateKeySize)
	if err := st.Update(context.Background(), OperatorSecretName, map[string][]byte{SecretKey: other}, first.RV+"stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale resourceVersion: %v", err)
	}
	again, err := st.Get(context.Background(), OperatorSecretName)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again.Data[SecretKey], first.Data[SecretKey]) {
		t.Fatal("a stale write replaced the key")
	}
}
