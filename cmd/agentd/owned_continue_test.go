package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestAwaitOwnedContinuationRequiresExplicitSamePodBudgetAuthority(t *testing.T) {
	for _, scenario := range []string{"continue", "later-deadline", "other-pod", "other-host", "other-task", "exhausted", "unconfirmed", "still-admitted", "wrong-epoch", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			b := apiv1.TaskBudgetBinding{TaskUID: "task", HostID: "host", PodUID: "pod", Epoch: 1, Deadline: time.Now().UTC().Add(time.Minute)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			next := b
			next.Epoch++
			status := apiv1.TaskBudgetStatus{Observed: true, Binding: next, StopConfirmed: true}
			switch scenario {
			case "later-deadline":
				status.Binding.Deadline = status.Binding.Deadline.Add(time.Second)
			case "other-pod":
				status.Binding.PodUID = "other"
			case "other-host":
				status.Binding.HostID = "other"
			case "other-task":
				status.Binding.TaskUID = "other"
			case "exhausted":
				status.Binding = b
				status.Latched = true
				status.Reason = "OverallBudget"
			case "unconfirmed":
				status.StopConfirmed = false
			case "still-admitted":
				status.Admitted = true
			case "wrong-epoch":
				status.Binding.Epoch++
			case "canceled":
				cancel()
			}
			calls := 0
			got, ok, err := awaitOwnedContinuation(ctx, func(context.Context, string) (apiv1.TaskBudgetStatus, error) { calls++; return status, nil }, b)
			if scenario == "continue" {
				if err != nil || !ok || got != next {
					t.Fatal(got, ok, err)
				}
			} else if ok {
				t.Fatal("unconfirmed/extended authority accepted", got)
			}
			if calls > 1 {
				t.Fatal("unexpected replay")
			}
		})
	}
}

func TestAwaitOwnedContinuationReadFailureNeverGrantsAuthority(t *testing.T) {
	b := apiv1.TaskBudgetBinding{TaskUID: "task", HostID: "host", PodUID: "pod", Epoch: 1, Deadline: time.Now().UTC().Add(time.Minute)}
	next := b
	next.Epoch++
	calls := 0
	got, ok, err := awaitOwnedContinuation(context.Background(), func(context.Context, string) (apiv1.TaskBudgetStatus, error) {
		calls++
		if calls == 1 {
			return apiv1.TaskBudgetStatus{}, errors.New("lost GET response")
		}
		return apiv1.TaskBudgetStatus{Observed: true, Binding: next, StopConfirmed: true}, nil
	}, b)
	if err != nil || !ok || got != next || calls != 2 {
		t.Fatal("safe GET retry", got, ok, err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, ok, err = awaitOwnedContinuation(ctx, func(context.Context, string) (apiv1.TaskBudgetStatus, error) {
		cancel()
		return apiv1.TaskBudgetStatus{}, errors.New("unknown status")
	}, b)
	if err != nil || ok {
		t.Fatal("unknown read became authority", ok, err)
	}
}
