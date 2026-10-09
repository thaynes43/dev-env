package keeper

import (
	"bytes"
	"errors"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// SSHCAValidation contains only bounded validation results, never key material,
// parser errors or paths. It does not prove node trust or provider access.
type SSHCAValidation struct {
	Version         int    `json:"version"`
	PrivateKeyValid bool   `json:"privateKeyValid"`
	PublicKeyValid  bool   `json:"publicKeyValid"`
	KeysMatch       bool   `json:"keysMatch"`
	Valid           bool   `json:"valid"`
	FailureCode     string `json:"failureCode"`
}

const (
	SSHCAUnavailable       = "Unavailable"
	SSHCAInvalidPrivateKey = "InvalidPrivateKey"
	SSHCAInvalidPublicKey  = "InvalidPublicKey"
	SSHCAKeyMismatch       = "KeyMismatch"
	SSHCAInvalidArguments  = "InvalidArguments"
)

// ValidateSSHCA reads only the local CA projection. It performs no Kubernetes,
// journal, leadership, certificate issuance or network operations.
func ValidateSSHCA(caDir string) SSHCAValidation {
	_, result, _ := loadSSHCA(caDir)
	return result
}

func loadSSHCA(caDir string) (ssh.Signer, SSHCAValidation, error) {
	result := SSHCAValidation{Version: 1, FailureCode: SSHCAUnavailable}
	// Resolve the projected mount once, keeping the private/public pair in one
	// generation while ExternalSecret atomically rotates its ..data link.
	dir, err := filepath.EvalSymlinks(caDir)
	if err != nil {
		return nil, result, errors.New("SSH CA files are unavailable")
	}
	// Kubernetes projected volumes keep the generation behind ..data. Plain test
	// directories have no ..data; those still read one stable directory.
	if generation, e := filepath.EvalSymlinks(filepath.Join(dir, "..data")); e == nil {
		dir = generation
	}
	raw, err := readBoundedFile(filepath.Join(dir, "private-key"), 16<<10)
	if err != nil {
		return nil, result, err
	}
	ca, err := ssh.ParsePrivateKey(raw)
	if err != nil || ca.PublicKey().Type() != ssh.KeyAlgoED25519 {
		result.FailureCode = SSHCAInvalidPrivateKey
		return nil, result, errors.New("SSH CA must be an unencrypted Ed25519 private key")
	}
	result.PrivateKeyValid = true
	pubraw, err := readBoundedFile(filepath.Join(dir, "public-key"), 8<<10)
	if err != nil {
		return nil, result, err
	}
	pub, _, opts, rest, err := ssh.ParseAuthorizedKey(pubraw)
	if err != nil || len(opts) > 0 || len(bytes.TrimSpace(rest)) > 0 || pub.Type() != ssh.KeyAlgoED25519 {
		result.FailureCode = SSHCAInvalidPublicKey
		return nil, result, errors.New("SSH CA public and private keys do not match")
	}
	result.PublicKeyValid = true
	if !bytes.Equal(pub.Marshal(), ca.PublicKey().Marshal()) {
		result.FailureCode = SSHCAKeyMismatch
		return nil, result, errors.New("SSH CA public and private keys do not match")
	}
	result.KeysMatch = true
	result.Valid = true
	result.FailureCode = ""
	return ca, result, nil
}
