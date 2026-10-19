package p104

import (
	"fmt"
	"strings"
)

func printReport(r Report) {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("P10.4 FAILURE / RECOVERY DEMO — REPORT")
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Printf("OK: %v\n", r.OK)
	if r.Error != "" {
		fmt.Printf("Error: %s\n", r.Error)
	}
	fmt.Printf("Seed: %d  PreCmds: %d  PostCmds: %d  Instruments: %d  Accounts: %d\n",
		r.Config.Seed, r.Config.PreCommands, r.Config.PostCommands, r.Config.Instruments, r.Config.Accounts)
	fmt.Println()
	if r.Config.RunF1 {
		printScenario(r.F1)
	}
	if r.Config.RunF2 {
		printScenario(r.F2)
	}
	fmt.Println("--- timeline ---")
	for _, e := range r.Timeline {
		fmt.Printf("T+%s  %s\n", e.T, e.Message)
	}
	fmt.Println()
	fmt.Println("--- limitations ---")
	for _, l := range r.Limitations {
		fmt.Printf("• %s\n", l)
	}
	fmt.Println()
	fmt.Println("Formal benchmarks: P7 owns formal benchmark.")
	fmt.Println("Do not treat as ACCEPTED — CTO decision only.")
	fmt.Println(strings.Repeat("═", 63))
}

func printScenario(p PrePost) {
	if p.Name == "" {
		return
	}
	fmt.Printf("── %s ──\n", p.Name)
	fmt.Printf("  Failed: %s → Promoted: %s\n", p.FailedNode, p.PromotedNode)
	fmt.Printf("  Term/Epoch: %d → %d\n", p.OldTerm, p.NewTerm)
	fmt.Printf("  Pre:  Event.Seq=%d Trade.Seq=%d P8.6=%v writers=%d\n",
		p.Pre.EventSeq, p.Pre.TradeSeqMax, p.Pre.P86OK, p.Pre.Writers)
	fmt.Printf("  Mid:  Event.Seq=%d Trade.Seq=%d P8.6=%v\n",
		p.Mid.EventSeq, p.Mid.TradeSeqMax, p.Mid.P86OK)
	fmt.Printf("  Post: Event.Seq=%d Trade.Seq=%d P8.6=%v\n",
		p.Post.EventSeq, p.Post.TradeSeqMax, p.Post.P86OK)
	fmt.Printf("  Stale reject=%v wal_ok=%v cash_ok=%v | Resume accept=%v wal_adv=%v\n",
		p.StaleRejected, p.StaleWALUnchanged, p.StaleCashUnchanged, p.NewAccepted, p.WALAdvanced)
	fmt.Printf("  Promotion=%dms Recovery=%dms HealWriters=%d OldAuth=%v 2ndRecovery=%v\n",
		p.PromotionMs, p.RecoveryMs, p.HealWriters, p.OldAuthoritative, p.SecondRecoveryOK)
	fmt.Println()
}
