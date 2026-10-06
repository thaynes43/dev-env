package agentd

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// fakeRunner records commands and answers them with handle, so tests never
// start claude or tmux.
type fakeRunner struct {
	mu     sync.Mutex
	calls  []Cmd
	handle func(c Cmd) (Result, error)
	// missing names commands LookPath does not find.
	missing map[string]bool
}

func (f *fakeRunner) Run(_ context.Context, c Cmd) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	h := f.handle
	f.mu.Unlock()
	if h == nil {
		return Result{}, nil
	}
	return h(c)
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if f.missing[name] {
		return "", errors.New("not found")
	}
	return "/fake/bin/" + name, nil
}

// lines renders each call as "name arg1 arg2 ...".
func (f *fakeRunner) lines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " "))
	}
	return out
}

// envOf is a getenv over a map.
func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}
