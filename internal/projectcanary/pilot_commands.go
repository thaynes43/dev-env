package projectcanary

import "strings"

// RendezvousCommandText is the command block both sealed prompts must contain
// before a future cohort. The shapes were proved on task image 2.29.5.
// JSON and EXACT_NAME are placeholders a reviewed prompt may replace.
// This text admits no session and binds no seal.
func RendezvousCommandText() string {
	return strings.Join([]string{
		"printf '%s' 'JSON' | /usr/local/bin/agentd task-claim-create",
		"/usr/local/bin/agentd task-claim-list --project dev-env",
		"/usr/local/bin/agentd inbox-list --project dev-env",
		"/usr/local/bin/agentd inbox-read --project dev-env --message EXACT_NAME",
		"/usr/local/bin/agentd inbox-ack --project dev-env --message EXACT_NAME",
	}, "\n")
}

// ValidateRendezvousCommands checks the two sealed prompts against the
// command shapes proved after cohorts r10 and r11. It is not operator
// admission: Validate still accepts a prompt that fails here. The rendezvous
// harness calls this and refuses dispatch.
func (p *PilotPolicy) ValidateRendezvousCommands() error {
	if p == nil || len(p.Prompts) != 2 {
		return ErrRefused
	}
	for _, prompt := range p.Prompts {
		if !validRendezvousPrompt(prompt) {
			return ErrRefused
		}
	}
	return nil
}

func validRendezvousPrompt(prompt string) bool {
	if containsWord(prompt, "stdin") || hasTrailingArg(prompt, "task-claim-create") || hasPositional(prompt, "inbox-read") || hasPositional(prompt, "inbox-ack") {
		return false
	}
	if !hasClaimPipeline(prompt) {
		return false
	}
	for _, line := range []string{
		"/usr/local/bin/agentd task-claim-list --project dev-env",
		"/usr/local/bin/agentd inbox-list --project dev-env",
	} {
		if !hasProjectCommand(prompt, line) {
			return false
		}
	}
	return hasMessageCommand(prompt, "/usr/local/bin/agentd inbox-read") && hasMessageCommand(prompt, "/usr/local/bin/agentd inbox-ack")
}

func hasClaimPipeline(prompt string) bool {
	const prefix = "printf '%s' '"
	const suffix = "' | /usr/local/bin/agentd task-claim-create"
	rest := prompt
	for {
		i := strings.Index(rest, prefix)
		if i < 0 {
			return false
		}
		after := rest[i+len(prefix):]
		end := strings.Index(after, suffix)
		if end > 0 && !strings.ContainsAny(after[:end], "\n\r'") && terminated(after[end+len(suffix):]) {
			return true
		}
		rest = rest[i+len(prefix):]
	}
}

// hasProjectCommand accepts the exact project command, or the same command
// with only further flag arguments. A longer project name does not match.
func hasProjectCommand(prompt, command string) bool {
	from := 0
	for {
		i := findBounded(prompt, command, from)
		if i < 0 {
			return false
		}
		after := prompt[i+len(command):]
		if after == "" || after[0] == '\n' || after[0] == '\r' || after[0] == ' ' || after[0] == '\t' {
			rest := strings.TrimLeft(after, " \t")
			if rest == "" || rest[0] == '\n' || rest[0] == '\r' || rest[0] == '-' {
				return true
			}
		}
		from = i + len(command)
	}
}

func hasMessageCommand(prompt, command string) bool {
	key := command + " --project dev-env --message "
	rest := prompt
	for {
		i := strings.Index(rest, key)
		if i < 0 {
			return false
		}
		after := rest[i+len(key):]
		n := 0
		for n < len(after) && after[n] != ' ' && after[n] != '\t' && after[n] != '\n' && after[n] != '\r' {
			n++
		}
		token := after[:n]
		if token != "" && !strings.EqualFold(token, "stdin") && terminated(after[n:]) {
			return true
		}
		rest = rest[i+len(key):]
	}
}

func hasTrailingArg(prompt, command string) bool {
	from := 0
	for {
		i := findBounded(prompt, command, from)
		if i < 0 {
			return false
		}
		if !terminated(prompt[i+len(command):]) {
			return true
		}
		from = i + len(command)
	}
}

func hasPositional(prompt, command string) bool {
	from := 0
	for {
		i := findBounded(prompt, command, from)
		if i < 0 {
			return false
		}
		after := prompt[i+len(command):]
		j := 0
		for j < len(after) && (after[j] == ' ' || after[j] == '\t') {
			j++
		}
		if j > 0 && j < len(after) && after[j] != '\n' && after[j] != '\r' && after[j] != '-' {
			return true
		}
		from = i + len(command)
	}
}

func terminated(after string) bool {
	i := 0
	for i < len(after) && (after[i] == ' ' || after[i] == '\t') {
		i++
	}
	return i == len(after) || after[i] == '\n' || after[i] == '\r'
}

func findBounded(s, command string, from int) int {
	for from <= len(s) {
		i := strings.Index(s[from:], command)
		if i < 0 {
			return -1
		}
		i += from
		before := i == 0 || !identByte(s[i-1])
		end := i + len(command)
		after := end == len(s) || !identByte(s[end])
		if before && after {
			return i
		}
		from = i + 1
	}
	return -1
}

func containsWord(s, word string) bool {
	s = strings.ToLower(s)
	word = strings.ToLower(word)
	from := 0
	for from <= len(s) {
		i := strings.Index(s[from:], word)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(word)
		left := i == 0 || !identByte(s[i-1])
		right := end == len(s) || !identByte(s[end])
		if left && right {
			return true
		}
		from = i + 1
	}
	return false
}

func identByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}
