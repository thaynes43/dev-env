package main

import (
	"slices"
	"strings"
	"testing"
)

func TestParentNotesFlagDefaultOffAndRequiresDurableInbox(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.parentNotes {
		t.Fatal("parent notes enabled by default", err)
	}
	if _, err := parseFlags([]string{"--enable-parent-notes"}); err == nil {
		t.Fatal("parent notes accepted without durable inbox")
	}
	bound := []string{"--enable-durable-inbox", "--inbox-projects=sample", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43"}
	o, err = parseFlags(bound)
	if err != nil || o.parentNotes {
		t.Fatal("durable inbox enabled parent notes", err)
	}
	o, err = parseFlags(append(slices.Clone(bound), "--enable-parent-notes"))
	if err != nil || !o.parentNotes || !o.inboxEnabled || o.inboxQuestions || o.coordinatorEnabled || len(o.inboxCoordinatorReadProjects) != 0 || o.parentNotice {
		t.Fatal("parent notes refused or widened another gate", err)
	}
}

func TestParentNoticeFlagDefaultOffAndRequiresParentNotes(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.parentNotice {
		t.Fatal("parent notice enabled by default", err)
	}
	if _, err := parseFlags([]string{"--enable-parent-notice"}); err == nil || !strings.Contains(err.Error(), "--enable-parent-notes") {
		t.Fatal("parent notice accepted without parent notes", err)
	}
	bound := []string{"--enable-durable-inbox", "--inbox-projects=sample", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43"}
	if _, err := parseFlags(append(slices.Clone(bound), "--enable-parent-notice")); err == nil || !strings.Contains(err.Error(), "--enable-parent-notes") {
		t.Fatal("parent notice accepted without parent notes", err)
	}
	if _, err := parseFlags(append(slices.Clone(bound), "--enable-parent-notes", "--enable-parent-notice", "--leader-elect=false")); err == nil || !strings.Contains(err.Error(), "--leader-elect") {
		t.Fatal("parent notice accepted without leader election", err)
	}
	o, err = parseFlags(append(slices.Clone(bound), "--enable-parent-notes", "--enable-parent-notice"))
	if err != nil || !o.parentNotice || !o.parentNotes || !o.inboxEnabled || !o.leaderElect {
		t.Fatal("parent notice refused when parent notes and leader election are on", err)
	}
}
