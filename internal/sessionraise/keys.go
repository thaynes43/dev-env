package sessionraise

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

const (
	// SecretKey is the Secret data key that holds the raw private key.
	SecretKey = "key"
	// PublicKeyField is the ConfigMap data key that holds the public key.
	PublicKeyField = "publicKey"
	// PublicIDField is the ConfigMap data key that holds the key id.
	PublicIDField = "keyId"

	// LedgerField is the ConfigMap data key that holds the ledger JSON.
	LedgerField = "ledger"
)

// Object names. GitOps precreates them. This package never creates them.
const (
	OperatorSecretName = "dev-env-session-raise-operator-key"
	OperatorPublicName = "dev-env-session-raise-operator-public"
	LedgerName         = "dev-env-session-raise-ledger"
	KeeperSecretName   = "dev-env-session-raise-keeper-key"
	KeeperPublicName   = "dev-env-session-raise-keeper-public"
	NoticeLedgerName   = "dev-env-keeper-raise-notices"
	ObjectNamespace    = "dev-env-system"
)

var (
	// ErrNoKey is an empty key Secret. POST is refused.
	ErrNoKey = errors.New("no raise key")
	// ErrUnreadable is a key or ledger that cannot be read. POST is refused.
	ErrUnreadable = errors.New("raise key unreadable")
	// ErrMismatch is a stored key that is not the published key. POST is refused
	// and the published key is left as it is.
	ErrMismatch = errors.New("raise key mismatch")
	// ErrConflict is a write whose resourceVersion lost.
	ErrConflict = errors.New("raise object conflict")
)

// Object is one Secret or ConfigMap the key and ledger code reads.
type Object struct {
	Data map[string][]byte
	RV   string
}

// Store reads and writes one named object with a resourceVersion.
// A missing object is ErrUnreadable: this package does not create it.
type Store interface {
	Get(ctx context.Context, name string) (Object, error)
	Update(ctx context.Context, name string, data map[string][]byte, rv string) error
}

// Load reads the private key and returns it only when it is the key just read
// and its public half is the published key. A replica signs with that key
// and with no other.
func Load(ctx context.Context, keys Store, secretName, publicName string) (ed25519.PrivateKey, error) {
	priv, err := readKey(ctx, keys, secretName)
	if err != nil {
		return nil, err
	}
	pub, err := readPublic(ctx, keys, publicName)
	if err != nil {
		return nil, err
	}
	if !pub.Equal(priv.Public()) {
		return nil, ErrMismatch
	}
	return priv, nil
}

// Ensure is the leader's first-start step. An empty Secret gets one key,
// written with the resourceVersion the leader read, so two leaders leave
// one key. The key returned is always the key read back after that write.
// An empty public ConfigMap is filled from that read-back key. A different
// published key is not overwritten.
func Ensure(ctx context.Context, keys Store, secretName, publicName string, leader bool) (ed25519.PrivateKey, error) {
	obj, err := keys.Get(ctx, secretName)
	if err != nil {
		return nil, err
	}
	if len(obj.Data[SecretKey]) == 0 && leader {
		_, fresh, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		data := map[string][]byte{SecretKey: append([]byte(nil), fresh...)}
		if err := keys.Update(ctx, secretName, data, obj.RV); err != nil && !errors.Is(err, ErrConflict) {
			return nil, err
		}
	}
	// The generated key is not used past this point. A replica that lost the
	// race, or one that wrote and then read a different value, signs only
	// what this read returns.
	priv, err := readKey(ctx, keys, secretName)
	if err != nil {
		return nil, err
	}
	if leader {
		if err := publish(ctx, keys, publicName, priv.Public().(ed25519.PublicKey)); err != nil {
			return nil, err
		}
	}
	return Load(ctx, keys, secretName, publicName)
}

func readKey(ctx context.Context, keys Store, name string) (ed25519.PrivateKey, error) {
	obj, err := keys.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	raw := obj.Data[SecretKey]
	if len(raw) == 0 {
		return nil, ErrNoKey
	}
	// A key counts only when its 64 bytes are exactly the key derived from
	// its first 32. Anything else, including a wrong length, is unreadable.
	if len(raw) != ed25519.PrivateKeySize {
		return nil, ErrUnreadable
	}
	want := ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize])
	if !bytes.Equal(raw, want) {
		return nil, ErrUnreadable
	}
	return ed25519.PrivateKey(append([]byte(nil), raw...)), nil
}

// ReadPublic is the published public key. An empty ConfigMap is ErrMismatch.
func ReadPublic(ctx context.Context, keys Store, name string) (ed25519.PublicKey, error) {
	return readPublic(ctx, keys, name)
}

func readPublic(ctx context.Context, keys Store, name string) (ed25519.PublicKey, error) {
	obj, err := keys.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	raw := obj.Data[PublicKeyField]
	if len(raw) == 0 {
		return nil, ErrMismatch
	}
	decoded, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, ErrMismatch
	}
	return ed25519.PublicKey(decoded), nil
}

func publish(ctx context.Context, keys Store, name string, pub ed25519.PublicKey) error {
	obj, err := keys.Get(ctx, name)
	if err != nil {
		return err
	}
	if len(obj.Data[PublicKeyField]) != 0 {
		have, err := readPublic(ctx, keys, name)
		if err != nil {
			return err
		}
		if !have.Equal(pub) {
			return ErrMismatch
		}
		return nil
	}
	data := map[string][]byte{
		PublicKeyField: []byte(base64.StdEncoding.EncodeToString(pub)),
		PublicIDField:  []byte(KeyID(pub)),
	}
	if err := keys.Update(ctx, name, data, obj.RV); err != nil && !errors.Is(err, ErrConflict) {
		return err
	}
	return nil
}
