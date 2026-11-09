package delegatedresult_test

import (
 "context"
 "testing"

 "github.com/thaynes43/dev-env/internal/delegatedresult"
)

// Compilation verifies the contracts are usable outside the owning package.
// These rejecting fixtures neither store data nor model durable atomicity.
type rejectingAdmission struct{}
func (rejectingAdmission) Resolve(context.Context, delegatedresult.Key) (delegatedresult.Key, delegatedresult.Provenance, error) {
 return delegatedresult.Key{}, delegatedresult.Provenance{}, delegatedresult.ErrDisabled
}

type rejectingStore struct{}
func (rejectingStore) Commit(_ context.Context, a delegatedresult.Attempt) (delegatedresult.Receipt, error) {
 _,_,_,_ = a.Values()
 return delegatedresult.Receipt{}, delegatedresult.ErrDisabled
}

func TestExternalInjectedBoundaries(t *testing.T) {
 var admission delegatedresult.Admission = rejectingAdmission{}
 var store delegatedresult.Store = rejectingStore{}
 if _,_,err:=admission.Resolve(context.Background(),delegatedresult.Key{});err!=delegatedresult.ErrDisabled {t.Fatal("fixture did not refuse")}
 if _,err:=store.Commit(context.Background(),delegatedresult.Attempt{});err!=delegatedresult.ErrDisabled {t.Fatal("fixture did not refuse")}
}
