package agentrun

import (
	"errors"
	"flag"
	"io"
	"strings"
)

// Output formats.
const (
	outputText = ""
	outputJSON = "json"
	outputName = "name"
)

// common are the flags every session command takes.
type common struct {
	output string
	conn   connOpts
}

// command is one verb's flag set and help.
type command struct {
	name string
	fs   *flag.FlagSet
	c    common
	// outputs are the -o values the verb takes besides text.
	outputs []string
}

func newCommand(name string, outputs ...string) *command {
	cmd := &command{name: name, fs: flag.NewFlagSet(name, flag.ContinueOnError), outputs: outputs}
	cmd.fs.SetOutput(io.Discard)
	cmd.fs.Usage = func() {}
	cmd.fs.StringVar(&cmd.c.output, "o", "", "")
	cmd.fs.StringVar(&cmd.c.output, "output", "", "")
	cmd.fs.StringVar(&cmd.c.conn.apiURL, "api-url", "", "")
	cmd.fs.StringVar(&cmd.c.conn.tokenFile, "token-file", "", "")
	cmd.fs.StringVar(&cmd.c.conn.caFile, "ca-file", "", "")
	cmd.fs.StringVar(&cmd.c.conn.kubeconfig, "kubeconfig", "", "")
	cmd.fs.StringVar(&cmd.c.conn.context, "context", "", "")
	return cmd
}

// parse parses flags and positional arguments in any order, as v1 did
// (agent-run haynes-ops -p "..." and agent-run -p "..." haynes-ops are the
// same). Everything after "--" is positional. It answers -h with the verb's
// help on stdout.
func (cmd *command) parse(a *app, args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		if err := cmd.fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				_, _ = io.WriteString(a.env.Stdout, commandHelp[cmd.name])
				return nil, &exitError{code: ExitOK}
			}
			return nil, usageError("%s: %v; agent-run help %s lists its flags", cmd.name, err, cmd.name)
		}
		rest := cmd.fs.Args()
		// flag stops at "--" and drops it; find out whether that is why.
		consumed := len(args) - len(rest)
		if consumed > 0 && args[consumed-1] == "--" {
			pos = append(pos, rest...)
			break
		}
		if len(rest) == 0 {
			break
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
	switch cmd.c.output {
	case outputText:
	default:
		ok := false
		for _, o := range cmd.outputs {
			ok = ok || o == cmd.c.output
		}
		if !ok {
			return nil, usageError("%s: -o %s is not a format it prints; it takes %s", cmd.name, cmd.c.output, outputList(cmd.outputs))
		}
	}
	return pos, nil
}

func outputList(outs []string) string {
	q := make([]string, 0, len(outs))
	for _, o := range outs {
		q = append(q, "-o "+o)
	}
	return strings.Join(q, " or ")
}
