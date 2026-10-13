package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const configPath = "/Users/thaynes/.kube/haynes-ops-machine.yaml"
const expectedContext = "haynes-haynes-ops-machine"

func configured(ctx context.Context) (*action, string) {
	f, err := os.Open(configPath)
	if err != nil {
		return nil, "ConfigUnavailable"
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(b) > 1024*1024 {
		return nil, "ConfigRefused"
	}
	return parseConfig(ctx, b)
}

// The existing reviewed static-config pattern: exactly one selected context,
// cluster/server and user/token; no exec/auth plugin, TLS override or proxy.
func parseConfig(ctx context.Context, b []byte) (*action, string) {
	var cfg struct {
		CurrentContext string `yaml:"current-context"`
		Contexts       []struct {
			Name    string            `yaml:"name"`
			Context map[string]string `yaml:"context"`
		} `yaml:"contexts"`
		Clusters []struct {
			Name    string         `yaml:"name"`
			Cluster map[string]any `yaml:"cluster"`
		} `yaml:"clusters"`
		Users []struct {
			Name string         `yaml:"name"`
			User map[string]any `yaml:"user"`
		} `yaml:"users"`
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	var extra any
	if len(b) > 1024*1024 || d.Decode(&cfg) != nil || d.Decode(&extra) != io.EOF || cfg.CurrentContext != expectedContext {
		return nil, "ContextRefused"
	}
	var c map[string]string
	n := 0
	for _, x := range cfg.Contexts {
		if x.Name == expectedContext {
			c, n = x.Context, n+1
		}
	}
	if n != 1 || c["cluster"] == "" || c["user"] == "" {
		return nil, "ContextRefused"
	}
	var cluster, user map[string]any
	nc, nu := 0, 0
	for _, x := range cfg.Clusters {
		if x.Name == c["cluster"] {
			cluster, nc = x.Cluster, nc+1
		}
	}
	for _, x := range cfg.Users {
		if x.Name == c["user"] {
			user, nu = x.User, nu+1
		}
	}
	if nc != 1 || nu != 1 || len(cluster) != 1 || len(user) != 1 {
		return nil, "AuthShapeRefused"
	}
	server, sok := cluster["server"].(string)
	token, tok := user["token"].(string)
	if !sok || !tok || token == "" || len(token) > 16384 || strings.ContainsAny(token, "\r\n\t ") {
		return nil, "AuthShapeRefused"
	}
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, "ServerShapeRefused"
	}
	return &action{ctx: ctx, base: u, token: token, tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12}}, ""
}
