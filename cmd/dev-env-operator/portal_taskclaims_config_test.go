package main

import (
	"slices"
	"testing"
)

func TestPortalTaskClaimsFlagsDefaultOff(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.portalTaskClaims.Enabled || len(o.portalTaskClaims.Projects) != 0 {
		t.Fatal("portal task claims enabled by default", err)
	}
	claims := []string{"--enable-task-claims", "--project-catalog=dev-env-system/dev-env-project-catalog", "--project-clone-owner=thaynes43", "--task-claims-read-projects=sample,other"}
	base := append(slices.Clone(claims), "--enable-portal-reader")
	o, err = parseFlags(base)
	if err != nil || o.portalTaskClaims.Enabled || len(o.portalTaskClaims.Projects) != 0 {
		t.Fatal("prerequisites enabled portal task claims", err)
	}
	o, err = parseFlags(append(slices.Clone(base), "--enable-portal-task-claims", "--portal-task-claims-projects=other"))
	if err != nil || !o.portalTaskClaims.Enabled || !slices.Equal(o.portalTaskClaims.Projects, []string{"other"}) || !slices.Equal(o.taskClaimReadProjects, []string{"sample", "other"}) {
		t.Fatal("explicit subset refused or changed claim scope", err)
	}
	refused := [][]string{
		{"--enable-portal-task-claims"},
		{"--portal-task-claims-projects=sample"},
		{"--enable-portal-task-claims", "--portal-task-claims-projects=sample"},
		append(slices.Clone(base), "--portal-task-claims-projects=sample"),
		append(slices.Clone(base), "--enable-portal-task-claims"),
		append(slices.Clone(base), "--enable-portal-task-claims", "--portal-task-claims-projects="),
		append(slices.Clone(claims), "--enable-portal-task-claims", "--portal-task-claims-projects=sample"),
		{"--enable-portal-reader", "--enable-portal-task-claims", "--portal-task-claims-projects=sample"},
		append(slices.Clone(base), "--enable-portal-task-claims", "--portal-task-claims-projects=unlisted"),
		append(slices.Clone(base), "--enable-portal-task-claims", "--portal-task-claims-projects=sample,unlisted"),
		append(slices.Clone(base), "--enable-portal-task-claims", "--portal-task-claims-projects=sample,sample"),
		append(slices.Clone(base), "--enable-portal-task-claims", "--portal-task-claims-projects=*"),
		append(slices.Clone(base), "--enable-portal-task-claims", "--portal-task-claims-projects=Sample"),
	}
	for _, args := range refused {
		if _, err := parseFlags(args); err == nil {
			t.Fatal("inconsistent portal task claims accepted", args)
		}
	}
}
