package testenv

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"k8s.io/client-go/util/keyutil"
)

// ExpiredServiceAccountToken re-signs one synthetic local envtest token with
// dates in the past. It uses ONLY this Env's ephemeral test-server signing key,
// never a kubeconfig, cluster Secret or provider credential. The returned token
// must remain inside the test; do not log it. TokenReview then proves expiry
// rejection independently of bad signature, audience or Pod identity.
func (e *Env) ExpiredServiceAccountToken(token string) (string, error) {
	if e == nil || e.env == nil || e.env.ControlPlane.APIServer == nil || e.env.ControlPlane.APIServer.CertDir == "" {
		return "", fmt.Errorf("local envtest is not running")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("expected synthetic compact token")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", err
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return "", err
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	claims["exp"] = time.Now().Add(-time.Hour).Unix()
	claims["iat"] = time.Now().Add(-2 * time.Hour).Unix()
	claims["nbf"] = time.Now().Add(-2 * time.Hour).Unix()
	payload, err = json.Marshal(claims)
	if err != nil {
		return "", err
	}
	key, err := keyutil.PrivateKeyFromFile(filepath.Join(e.env.ControlPlane.APIServer.CertDir, "sa-signer.key"))
	if err != nil {
		return "", err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.SignatureAlgorithm(header.Alg), Key: key}, (&jose.SignerOptions{}).WithHeader("kid", header.Kid))
	if err != nil {
		return "", err
	}
	signed, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return signed.CompactSerialize()
}
