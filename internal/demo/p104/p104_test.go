package p104_test

import (
	"os"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/demo/p104"
)

func TestMain(m *testing.M) {
	if os.Getenv(p104.EnvChildPrimary) == "1" {
		os.Exit(p104.RunPrimaryChild())
	}
	os.Exit(m.Run())
}

func TestP104_F1_Partition_Failover(t *testing.T) {
	cfg := p104.Defaults()
	cfg.RootDir = t.TempDir()
	cfg.PreCommands = 60
	cfg.PostCommands = 40
	cfg.RunF1 = true
	cfg.RunF2 = false
	cfg.RunF3 = false
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
}

func TestP104_F2_Promote(t *testing.T) {
	cfg := p104.Defaults()
	cfg.RootDir = t.TempDir()
	cfg.PreCommands = 60
	cfg.PostCommands = 40
	cfg.RunF1 = false
	cfg.RunF2 = true
	cfg.RunF3 = false
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
}

func TestP104_F3_RealProcessKill(t *testing.T) {
	cfg := p104.Defaults()
	cfg.RootDir = t.TempDir()
	cfg.PreCommands = 50
	cfg.PostCommands = 30
	cfg.RunF1 = false
	cfg.RunF2 = false
	cfg.RunF3 = true
	rep, err := p104.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatal(rep.Error)
	}
	f := rep.F3
	if !f.StaleRejected || !f.StaleWALUnchanged {
		t.Fatal("stale after kill")
	}
	if !f.NewAccepted || !f.WALAdvanced {
		t.Fatal("resume after kill")
	}
	if !f.Pre.P86OK || !f.Mid.P86OK || !f.Post.P86OK {
		t.Fatalf("p86 pre=%v mid=%v post=%v midErr=%s postErr=%s",
			f.Pre.P86OK, f.Mid.P86OK, f.Post.P86OK, f.Mid.P86Err, f.Post.P86Err)
	}
	if f.Post.EventSeq <= f.Pre.EventSeq {
		t.Fatal("seq regression after process kill")
	}
	if !f.OldRestarted || f.OldAuthoritative {
		t.Fatal("old process heal")
	}
	if !f.SecondRecoveryOK {
		t.Fatal("2nd recovery")
	}
}

func TestP104_All(t *testing.T) {
	cfg := p104.Defaults()
	cfg.RootDir = t.TempDir()
	cfg.PreCommands = 40
	cfg.PostCommands = 25
	rep, err := p104.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK {
		t.Fatal(rep.Error)
	}
}
