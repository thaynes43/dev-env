package main

import (
	"os/exec"
	"strings"
)

type capability struct {
	Schema                    string `json:"schema"`
	SourceCommit              string `json:"sourceCommit"`
	Release                   string `json:"release"`
	CLI                       string `json:"cli"`
	Project                   string `json:"project"`
	Profile                   string `json:"profile"`
	PublicationRoute          string `json:"publicationRoute"`
	PublicationActivation     string `json:"publicationActivation"`
	HistoryRoute              string `json:"historyRoute"`
	HistoryActivation         string `json:"historyActivation"`
	RescueRoute               string `json:"rescueRoute"`
	RescueActivation          string `json:"rescueActivation"`
	CallerScope               string `json:"callerScope"`
	TaskOwnership             string `json:"taskOwnership"`
	OperatorSettings          string `json:"operatorSettings"`
	SourcePresence            string `json:"sourcePresence"`
	DeployedCapability        bool   `json:"deployedCapability"`
	InstalledDrill            string `json:"installedDrill"`
	H7RequiredForExcerptProof bool   `json:"h7RequiredForExcerptProof"`
	BlockedOperation          string `json:"blockedOperation"`
	BlockedOperationOwner     string `json:"blockedOperationOwner"`
	Network                   string `json:"network"`
	Model                     string `json:"model"`
	LiveStop                  string `json:"liveStop"`
	LiveRead                  string `json:"liveRead"`
	LiveRestore               string `json:"liveRestore"`
	LiveAssignment            string `json:"liveAssignment"`
}

func preflight(dir string) capability {
	report := capability{
		Schema:                    "dev-env.stopped-handoff-capability/v1",
		SourceCommit:              "unknown",
		Release:                   "unknown",
		CLI:                       "stopped_handoff",
		Project:                   "dev-env",
		Profile:                   "not-selected",
		PublicationRoute:          "explicit-excerpt-named",
		PublicationActivation:     "unknown",
		HistoryRoute:              "archived-history-cli-named",
		HistoryActivation:         "unknown",
		RescueRoute:               "code-rescue-named",
		RescueActivation:          "unknown",
		CallerScope:               "source-fixture-only",
		TaskOwnership:             "openproject-101-source-validation",
		OperatorSettings:          "not-read",
		SourcePresence:            "checker-present",
		DeployedCapability:        false,
		InstalledDrill:            "not-performed",
		H7RequiredForExcerptProof: false,
		BlockedOperation:          blockedOperation,
		BlockedOperationOwner:     blockedOwner,
		Network:                   "not-used",
		Model:                     "not-used",
		LiveStop:                  "not-performed",
		LiveRead:                  "not-performed",
		LiveRestore:               "not-performed",
		LiveAssignment:            "not-performed",
	}
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	cmd.Env = fixtureEnv()
	out, err := cmd.Output()
	if err != nil {
		return report
	}
	head := strings.TrimRight(string(out), "\r\n")
	if validHead(head) {
		report.SourceCommit = head
	}
	return report
}
