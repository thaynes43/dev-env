package main

import (
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/thaynes43/dev-env/internal/coordretention"
	"github.com/thaynes43/dev-env/internal/shelf"
)

func TestCoordinationRetentionFlagStaysOffAndRefusesUnsafeStart(t *testing.T) {
	o, err := parseFlags(nil)
	if err != nil || o.coordinationRetention || o.coordinationArchiveReads || o.coordinationRetentionGrace != coordretention.DefaultGrace {
		t.Fatalf("defaults: retention %v reads %v grace %s err %v", o.coordinationRetention, o.coordinationArchiveReads, o.coordinationRetentionGrace, err)
	}
	if got := newCoordinationRetention(o, nil, nil, nil, nil, nil, logr.Discard()); got != nil {
		t.Fatal("flag off built a retention runnable")
	}
	if _, err := parseFlags([]string{"--enable-coordination-retention"}); err == nil {
		t.Fatal("retention on with archive reads off was accepted")
	}
	if _, err := parseFlags([]string{"--enable-coordination-retention", "--enable-coordination-archive-reads=false"}); err == nil {
		t.Fatal("retention on with explicit reads off was accepted")
	}
	for _, grace := range []string{"719h59m", "24h", "0s"} {
		if _, err := parseFlags([]string{"--coordination-retention-grace", grace}); err == nil {
			t.Fatalf("grace %s under 720h was accepted", grace)
		}
	}
	o, err = parseFlags([]string{"--enable-coordination-archive-reads", "--enable-coordination-retention", "--coordination-retention-grace", "1000h"})
	if err != nil || !o.coordinationRetention || !o.coordinationArchiveReads || o.coordinationRetentionGrace != 1000*time.Hour {
		t.Fatalf("explicit retention: retention %v reads %v grace %s err %v", o.coordinationRetention, o.coordinationArchiveReads, o.coordinationRetentionGrace, err)
	}
	o.taskClaimsEnabled = true
	got := newCoordinationRetention(o, nil, nil, nil, nil, shelf.NewExecSlots(shelf.DefaultExecSlots), logr.Discard())
	if got == nil || !got.Enabled || got.Grace != 1000*time.Hour || !got.Kinds[coordretention.TaskClaim] || got.Kinds[coordretention.InboxMessage] {
		t.Fatalf("runnable = %#v", got)
	}
}

var (
	_ coordretention.PodSource = (*shelf.Shelf)(nil)
	_ coordretention.Archive   = (*shelf.CoordinationArchive)(nil)
)
