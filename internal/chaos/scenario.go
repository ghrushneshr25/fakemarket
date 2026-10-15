package chaos

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Scenario is a reproducible chaos case (serializable for CI artifacts).
type Scenario struct {
	Name   string `json:"name"`
	Seed   int64  `json:"seed"`
	WALDir string `json:"wal_dir,omitempty"` // set by runner
	// Commands is a simple deterministic submit count for harness self-tests.
	// Full trading command streams come in later milestones.
	CommandCount int    `json:"command_count"`
	Instrument   string `json:"instrument"`
	// Faults scheduled against the injector.
	Faults []Plan `json:"faults"`
	// ExpectCrash if a CRASH action is expected.
	ExpectCrash bool `json:"expect_crash"`
	// ExpectDurableMaxSeq is the max Event.Sequence that must appear after recovery
	// (0 = no check). Used for group-commit boundary tests.
	ExpectDurableMaxSeq uint64 `json:"expect_durable_max_seq,omitempty"`
	// ExpectNotDurableMinSeq if set, recovery must NOT have sequences >= this
	// when crash was before sync of that batch (optional).
	ExpectNotDurableMinSeq uint64 `json:"expect_not_durable_min_seq,omitempty"`
}

// Result is the outcome of one scenario run.
type Result struct {
	Scenario    Scenario  `json:"scenario"`
	Hits        []Hit     `json:"hits"`
	Crashed     bool      `json:"crashed"`
	Restarted   bool      `json:"restarted"`
	Recovered   bool      `json:"recovered"`
	LastSeq     uint64    `json:"last_sequence_after_recovery"`
	EventsRead  uint64    `json:"events_read"`
	Err         string    `json:"error,omitempty"`
	Started     time.Time `json:"started"`
	Finished    time.Time `json:"finished"`
	ArtifactDir string    `json:"artifact_dir,omitempty"`
}

// WriteArtifacts persists scenario + result for reproducibility.
func WriteArtifacts(dir string, sc Scenario, res Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	write := func(name string, v any) error {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, name), b, 0o644)
	}
	if err := write("scenario.json", sc); err != nil {
		return err
	}
	if err := write("result.json", res); err != nil {
		return err
	}
	seed := fmt.Sprintf("seed=%d\nname=%s\n", sc.Seed, sc.Name)
	return os.WriteFile(filepath.Join(dir, "seed.txt"), []byte(seed), 0o644)
}
