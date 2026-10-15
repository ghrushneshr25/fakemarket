package chaos

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// Storage fault kinds for FileOps decorator (P8.3).
const (
	StorFailWrite      = "write"
	StorFailFlush      = "flush"
	StorFailSync       = "sync"
	StorFailOpen       = "open"
	StorFailShortWrite = "short_write"
	StorFailENOSPC     = "enospc"
)

// StoragePlan schedules a FileOps failure on the Nth call of an op (1-based).
type StoragePlan struct {
	Op         string // write|flush|sync|open|short_write|enospc
	Occurrence int    // 1-based; default 1
	// AfterHeader: if true, skip failures until segment header open/sync completed once.
	AfterHeader bool
}

// StorageFaults is a concurrent-safe FileOps factory for deterministic storage errors.
type StorageFaults struct {
	mu       sync.Mutex
	plans    []StoragePlan
	counts   map[string]int // op -> invocations (all)
	postHdr  map[string]int // op -> invocations after header OK
	headerOK bool
	// RealDir when set, wraps real os files under Dir (integration).
	// When empty, uses in-memory buffer FileOps (unit).
	RealDir string
}

// NewStorageFaults builds an empty injector (no-op until Arm).
func NewStorageFaults() *StorageFaults {
	return &StorageFaults{counts: make(map[string]int), postHdr: make(map[string]int)}
}

// Arm replaces plans.
func (s *StorageFaults) Arm(plans ...StoragePlan) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans = append([]StoragePlan(nil), plans...)
	s.counts = make(map[string]int)
	s.postHdr = make(map[string]int)
	s.headerOK = false
}

// OpenFile returns a wal.OpenFile-compatible opener.
func (s *StorageFaults) OpenFile(path string) (wal.FileOps, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bump("open")
	if err := s.matchLocked("open"); err != nil {
		return nil, err
	}
	var inner wal.FileOps
	var err error
	if s.RealDir != "" {
		inner, err = wal.DefaultOpenFile(path)
	} else {
		inner = &memFile{name: path}
	}
	if err != nil {
		return nil, err
	}
	return &faultyStorageFile{inner: inner, sf: s}, nil
}

func (s *StorageFaults) bump(op string) {
	s.counts[op]++
	if s.headerOK {
		s.postHdr[op]++
	}
}

func (s *StorageFaults) matchLocked(op string) error {
	for _, p := range s.plans {
		planOp := p.Op
		if planOp == StorFailENOSPC {
			if op != "write" {
				continue
			}
		} else if planOp == StorFailShortWrite {
			continue // handled in Write()
		} else if planOp != op {
			continue
		}
		occ := p.Occurrence
		if occ <= 0 {
			occ = 1
		}
		if p.AfterHeader && !s.headerOK {
			continue
		}
		cn := s.counts[op]
		if p.AfterHeader {
			cn = s.postHdr[op]
		}
		if cn != occ {
			continue
		}
		switch planOp {
		case StorFailENOSPC:
			return fmt.Errorf("%w: %w", wal.ErrWriteFailed, syscall.ENOSPC)
		default:
			return fmt.Errorf("chaos storage fail op=%s occ=%d", op, occ)
		}
	}
	return nil
}

func (s *StorageFaults) noteHeaderOK() {
	s.mu.Lock()
	s.headerOK = true
	s.mu.Unlock()
}

type faultyStorageFile struct {
	inner wal.FileOps
	sf    *StorageFaults
	wrote int
}

func (f *faultyStorageFile) Write(p []byte) (int, error) {
	f.sf.mu.Lock()
	f.sf.bump("write")
	// short write plan
	for _, pl := range f.sf.plans {
		if pl.Op != StorFailShortWrite {
			continue
		}
		occ := pl.Occurrence
		if occ <= 0 {
			occ = 1
		}
		if pl.AfterHeader && !f.sf.headerOK {
			continue
		}
		cn := f.sf.counts["write"]
		if pl.AfterHeader {
			cn = f.sf.postHdr["write"]
		}
		if cn == occ && len(p) > 1 {
			f.sf.mu.Unlock()
			n := len(p) / 2
			if n == 0 {
				n = 1
			}
			wn, _ := f.inner.Write(p[:n])
			return wn, io.ErrShortWrite
		}
	}
	err := f.sf.matchLocked("write")
	f.sf.mu.Unlock()
	if err != nil {
		return 0, err
	}
	n, err := f.inner.Write(p)
	f.wrote += n
	return n, err
}

func (f *faultyStorageFile) Flush() error {
	f.sf.mu.Lock()
	f.sf.bump("flush")
	err := f.sf.matchLocked("flush")
	f.sf.mu.Unlock()
	if err != nil {
		return err
	}
	return f.inner.Flush()
}

func (f *faultyStorageFile) Sync() error {
	f.sf.mu.Lock()
	f.sf.bump("sync")
	err := f.sf.matchLocked("sync")
	f.sf.mu.Unlock()
	if err != nil {
		return err
	}
	if err := f.inner.Sync(); err != nil {
		return err
	}
	f.sf.noteHeaderOK()
	return nil
}

func (f *faultyStorageFile) Close() error { return f.inner.Close() }
func (f *faultyStorageFile) Name() string { return f.inner.Name() }

// memFile is an in-memory FileOps for unit tests (no real disk).
type memFile struct {
	name string
	buf  []byte
}

func (m *memFile) Write(p []byte) (int, error) {
	m.buf = append(m.buf, p...)
	return len(p), nil
}
func (m *memFile) Flush() error { return nil }
func (m *memFile) Sync() error  { return nil }
func (m *memFile) Close() error { return nil }
func (m *memFile) Name() string { return m.name }

// InstallStorageOpen wires StorageFaults into wal.Config.OpenFile.
func InstallStorageOpen(cfg *wal.Config, sf *StorageFaults) {
	if cfg == nil || sf == nil {
		return
	}
	cfg.OpenFile = sf.OpenFile
}

// CorruptWAL mutates a segment file and returns a description for artifacts.
type CorruptionReport struct {
	Segment string `json:"segment"`
	Offset  int64  `json:"offset"`
	Kind    string `json:"kind"`
	Before  []byte `json:"before,omitempty"`
	After   []byte `json:"after,omitempty"`
}

// CorruptFileAt XORs one byte at offset (or appends partial tail).
func CorruptFileAt(path string, offset int64, kind string) (CorruptionReport, error) {
	rep := CorruptionReport{Segment: filepath.Base(path), Offset: offset, Kind: kind}
	b, err := os.ReadFile(path)
	if err != nil {
		return rep, err
	}
	if kind == "truncate_tail" {
		if len(b) < 10 {
			return rep, fmt.Errorf("file too small")
		}
		rep.Before = append([]byte(nil), b[len(b)-8:]...)
		nb := b[:len(b)-5]
		rep.After = nb[len(nb)-3:]
		return rep, os.WriteFile(path, nb, 0o600)
	}
	if offset < 0 || int(offset) >= len(b) {
		return rep, fmt.Errorf("offset out of range")
	}
	rep.Before = []byte{b[offset]}
	b[offset] ^= 0xFF
	rep.After = []byte{b[offset]}
	return rep, os.WriteFile(path, b, 0o600)
}

// ListSegments returns segment paths in dir.
func ListSegments(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() && len(e.Name()) > 4 {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}
