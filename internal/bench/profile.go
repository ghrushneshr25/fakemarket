package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"time"
)

// ProfileConfig selects which pprof kinds to capture during a steady-state run.
// Measurement-only (P7.5) — does not change production code paths.
type ProfileConfig struct {
	// Dir is the output directory (created if missing).
	Dir string
	// Label prefixes filenames: {label}_cpu.pprof etc.
	Label string
	// CPU enables CPU profiling for the timed section.
	CPU bool
	// Mem writes a heap profile after the section (in-use + allocs via MemProfileRate).
	Mem bool
	// Mutex enables mutex profile (SetMutexProfileFraction).
	Mutex bool
	// Block enables block profile (SetBlockProfileRate).
	Block bool
	// MutexFraction passed to runtime.SetMutexProfileFraction (1 = sample all).
	MutexFraction int
	// BlockRate ns blocking events (1 = sample all block events; costly).
	BlockRate int
}

// ProfileSession holds state while profiling is active.
type ProfileSession struct {
	cfg        ProfileConfig
	cpuFile    *os.File
	prevMutex  int
	prevBlock  int
	startedCPU bool
}

// Start begins CPU/mutex/block collection. Call Stop after the steady-state workload.
func Start(cfg ProfileConfig) (*ProfileSession, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("profile: Dir required")
	}
	if cfg.Label == "" {
		cfg.Label = "run"
	}
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, err
	}
	s := &ProfileSession{cfg: cfg}
	if cfg.Mutex {
		frac := cfg.MutexFraction
		if frac <= 0 {
			frac = 1
		}
		s.prevMutex = runtime.SetMutexProfileFraction(frac)
	}
	if cfg.Block {
		rate := cfg.BlockRate
		if rate <= 0 {
			rate = 1
		}
		runtime.SetBlockProfileRate(rate)
		s.prevBlock = rate // restore to 0 on stop
	}
	if cfg.CPU {
		path := filepath.Join(cfg.Dir, cfg.Label+"_cpu.pprof")
		f, err := os.Create(path)
		if err != nil {
			s.restore()
			return nil, err
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			_ = f.Close()
			s.restore()
			return nil, err
		}
		s.cpuFile = f
		s.startedCPU = true
	}
	if cfg.Mem {
		// Ensure allocation sampling is on for allocs profile.
		if runtime.MemProfileRate == 0 {
			runtime.MemProfileRate = 512 * 1024
		}
	}
	return s, nil
}

// Stop ends CPU profile and writes heap/mutex/block artifacts.
func (s *ProfileSession) Stop() error {
	if s == nil {
		return nil
	}
	var first error
	if s.startedCPU {
		pprof.StopCPUProfile()
		if s.cpuFile != nil {
			if err := s.cpuFile.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	if s.cfg.Mem {
		runtime.GC() // stable heap snapshot for inuse
		if err := writeProfile(filepath.Join(s.cfg.Dir, s.cfg.Label+"_heap.pprof"), "heap"); err != nil && first == nil {
			first = err
		}
		if err := writeProfile(filepath.Join(s.cfg.Dir, s.cfg.Label+"_allocs.pprof"), "allocs"); err != nil && first == nil {
			first = err
		}
	}
	if s.cfg.Mutex {
		if err := writeProfile(filepath.Join(s.cfg.Dir, s.cfg.Label+"_mutex.pprof"), "mutex"); err != nil && first == nil {
			first = err
		}
	}
	if s.cfg.Block {
		if err := writeProfile(filepath.Join(s.cfg.Dir, s.cfg.Label+"_block.pprof"), "block"); err != nil && first == nil {
			first = err
		}
	}
	s.restore()
	// Sidecar meta
	meta := fmt.Sprintf("label=%s\nfinished=%s\n", s.cfg.Label, time.Now().UTC().Format(time.RFC3339))
	_ = os.WriteFile(filepath.Join(s.cfg.Dir, s.cfg.Label+"_meta.txt"), []byte(meta), 0o644)
	return first
}

func (s *ProfileSession) restore() {
	if s.cfg.Mutex {
		runtime.SetMutexProfileFraction(s.prevMutex)
	}
	if s.cfg.Block {
		runtime.SetBlockProfileRate(0)
	}
}

func writeProfile(path, name string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	p := pprof.Lookup(name)
	if p == nil {
		return fmt.Errorf("pprof profile %q not found", name)
	}
	return p.WriteTo(f, 0)
}

// DefaultProfileDir is under docs/benchmarks/profiles/out (gitignored binaries).
// Resolves module root so go test from internal/bench still writes correctly.
func DefaultProfileDir() string {
	if d := os.Getenv("PROFILE_DIR"); d != "" {
		return d
	}
	root := findModuleRoot()
	if root == "" {
		return filepath.Join("docs", "benchmarks", "profiles", "out")
	}
	return filepath.Join(root, "docs", "benchmarks", "profiles", "out")
}

func findModuleRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for d := wd; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		if d == filepath.Dir(d) {
			return ""
		}
	}
}
