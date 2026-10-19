package p104_test

import (
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/demo/p104"
)

func TestP104_F1_Partition_Failover(t *testing.T) {
	cfg := p104.Defaults()
	cfg.RootDir = t.TempDir()
	cfg.PreCommands = 60
	cfg.PostCommands = 40
	cfg.RunF1 = true
	cfg.RunF2 = false
	rep, err := p104.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatal(rep.Error)
	}
	f := rep.F1
	if !f.StaleRejected || !f.StaleWALUnchanged || !f.StaleCashUnchanged {
		t.Fatal("stale safety")
	}
	if !f.NewAccepted || !f.WALAdvanced {
		t.Fatal("resume")
	}
	if !f.Pre.P86OK || !f.Mid.P86OK || !f.Post.P86OK {
		t.Fatal("p86")
	}
	if f.Post.EventSeq <= f.Pre.EventSeq {
		t.Fatal("seq regression")
	}
	if f.HealWriters != 1 || f.OldAuthoritative {
		t.Fatal("split brain")
	}
	if !f.SecondRecoveryOK {
		t.Fatal("2nd recovery")
	}
}

func TestP104_F2_Promote(t *testing.T) {
	cfg := p104.Defaults()
	cfg.RootDir = t.TempDir()
	cfg.PreCommands = 60
	cfg.PostCommands = 40
	cfg.RunF1 = false
	cfg.RunF2 = true
	rep, err := p104.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatal(rep.Error)
	}
	f := rep.F2
	if !f.StaleRejected || !f.StaleWALUnchanged {
		t.Fatal("stale")
	}
	if !f.NewAccepted || !f.WALAdvanced {
		t.Fatal("resume")
	}
	if !f.Pre.P86OK || !f.Post.P86OK {
		t.Fatal("p86")
	}
	if f.Post.EventSeq <= f.Pre.EventSeq {
		t.Fatal("seq")
	}
}

func TestP104_Both(t *testing.T) {
	cfg := p104.Defaults()
	cfg.RootDir = t.TempDir()
	cfg.PreCommands = 40
	cfg.PostCommands = 30
	rep, err := p104.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatal(rep.Error)
	}
}
