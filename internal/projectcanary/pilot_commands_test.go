package projectcanary_test

import (
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func TestRendezvousCommandsAcceptProvedShapesOnly(t *testing.T) {
	block := projectcanary.RendezvousCommandText()
	message := "message.ce4a9f8a21da2153afba4e115e643770.555066cd2a8dc46506572a4da482f962"
	real := strings.ReplaceAll(block, "JSON", `{"identity":{"project":"dev-env","repository":"dev-env","taskKey":"task:example"}}`)
	real = strings.ReplaceAll(real, "EXACT_NAME", message)
	withLimit := strings.ReplaceAll(block, "task-claim-list --project dev-env", "task-claim-list --project dev-env --limit 1")
	withLimit = strings.ReplaceAll(withLimit, "inbox-list --project dev-env", "inbox-list --project dev-env --limit 1")
	for _, body := range []string{block, "Follow the phase budget.\n" + block + "\nStop at the deadline.", real, withLimit} {
		if err := (&projectcanary.PilotPolicy{Prompts: []string{body, body}}).ValidateRendezvousCommands(); err != nil {
			t.Fatalf("proved shape refused: %v\n%s", err, body)
		}
	}
	bad := []string{
		"short prompt without commands",
		strings.ReplaceAll(block, "printf '%s' 'JSON' | /usr/local/bin/agentd task-claim-create", "/usr/local/bin/agentd task-claim-create stdin"),
		block + "\n/usr/local/bin/agentd task-claim-create stdin",
		block + "\n/usr/local/bin/agentd task-claim-create --timeout=10s",
		strings.ReplaceAll(block, "/usr/local/bin/agentd inbox-read --project dev-env --message EXACT_NAME", "/usr/local/bin/agentd inbox-read "+message),
		block + "\n/usr/local/bin/agentd inbox-read " + message,
		block + "\n/usr/local/bin/agentd inbox-ack " + message,
		block + "\nThe word STDIN must not be an argument.",
		strings.ReplaceAll(block, "/usr/local/bin/agentd inbox-ack --project dev-env --message EXACT_NAME", "/usr/local/bin/agentd inbox-ack --message EXACT_NAME --project dev-env"),
		strings.ReplaceAll(block, "task-claim-list --project dev-env", "task-claim-list --project dev-env-other"),
		block + "\n/usr/local/bin/agentd inbox-read --project dev-env " + message,
		block + "\n/usr/local/bin/agentd inbox-ack --project dev-env --message " + message + " extra",
	}
	for _, body := range bad {
		if err := (&projectcanary.PilotPolicy{Prompts: []string{body, body}}).ValidateRendezvousCommands(); err == nil {
			t.Fatalf("bad shape accepted:\n%s", body)
		}
	}
	good := &projectcanary.PilotPolicy{Prompts: []string{block, "actor B without the command block"}}
	if good.ValidateRendezvousCommands() == nil {
		t.Fatal("one slot without the commands was accepted")
	}
	if (&projectcanary.PilotPolicy{}).ValidateRendezvousCommands() == nil {
		t.Fatal("empty policy accepted")
	}
}

func TestOperatorAdmissionIgnoresRendezvousCommandContract(t *testing.T) {
	p := testutil.NewPilot(t).Policy
	if p.Validate() != nil {
		t.Fatal("short synthetic prompts must stay admissible")
	}
	if p.ValidateRendezvousCommands() == nil {
		t.Fatal("admission fixture passed the rendezvous command contract")
	}
}
