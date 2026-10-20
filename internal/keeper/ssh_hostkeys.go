package keeper

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // known_hosts hashing is defined as HMAC-SHA1; this is a name match, not a security primitive.
	"encoding/base64"
	"net"
	"path"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// hostKeyAlgorithms returns the host-key signature algorithms to offer for one
// target, derived from the keys pinned for it in known_hosts.
//
// x/crypto offers its own default order (ECDSA before ED25519) and the server
// answers with the first it supports. A node serves ECDSA, RSA and ED25519 keys
// while the trust file pins only ED25519, so without this the server presents
// an unpinned ECDSA key and the handshake dies before authentication.
//
// This only narrows what the client asks for. The knownhosts callback still
// verifies the presented key against the pin, so a changed or unpinned key is
// still refused. When the file has no usable plain entry for the target, nil is
// returned and the library default applies: the callback then refuses as before.
func hostKeyAlgorithms(knownHosts []byte, target string) []string {
	host := knownhosts.Normalize(target)
	var out []string
	seen := map[string]bool{}
	scanner := bufio.NewScanner(bytes.NewReader(knownHosts))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		marker, hosts, key, _, _, err := ssh.ParseKnownHosts(line)
		// Only plain pins count. @cert-authority and @revoked lines are not host keys.
		if err != nil || marker != "" || key == nil || !hostsMatch(hosts, host) {
			continue
		}
		for _, algo := range algorithmsForKey(key) {
			if !seen[algo] {
				seen[algo] = true
				out = append(out, algo)
			}
		}
	}
	return out
}

func algorithmsForKey(key ssh.PublicKey) []string {
	if key.Type() == ssh.KeyAlgoRSA {
		// A modern server does not offer the SHA-1 ssh-rsa signature.
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	return []string{key.Type()}
}

// hostsMatch reports whether the known_hosts host field names host. It covers
// the forms the keeper's trust file can hold: plain names, comma lists, glob
// patterns and |1| hashed names. A negated pattern excludes the line.
func hostsMatch(field []string, host string) bool {
	matched := false
	for _, pattern := range field {
		pattern = strings.TrimSpace(pattern)
		switch {
		case pattern == "":
		case strings.HasPrefix(pattern, "!"):
			if patternMatches(pattern[1:], host) {
				return false
			}
		case patternMatches(pattern, host):
			matched = true
		}
	}
	return matched
}

func patternMatches(pattern, host string) bool {
	if strings.HasPrefix(pattern, "|1|") {
		parts := strings.Split(pattern, "|")
		if len(parts) != 4 {
			return false
		}
		salt, e1 := base64.StdEncoding.DecodeString(parts[2])
		want, e2 := base64.StdEncoding.DecodeString(parts[3])
		if e1 != nil || e2 != nil {
			return false
		}
		mac := hmac.New(sha1.New, salt) //nolint:gosec // see import note.
		_, _ = mac.Write([]byte(host))
		return hmac.Equal(mac.Sum(nil), want)
	}
	if strings.ContainsAny(pattern, "*?") {
		ok, err := path.Match(pattern, host)
		return err == nil && ok
	}
	if h, p, err := net.SplitHostPort(pattern); err == nil {
		// "[host]:port" entries normalise the same way as the target does.
		return knownhosts.Normalize(net.JoinHostPort(h, p)) == host
	}
	return pattern == host
}
