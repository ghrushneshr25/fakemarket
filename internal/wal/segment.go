package wal

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

// segmentNameRe matches segment-00000001.wal exactly (8-digit zero-padded index).
var segmentNameRe = regexp.MustCompile(`^segment-(\d{8})\.wal$`)

// segmentInfo is one authoritative WAL segment file.
type segmentInfo struct {
	Index uint64
	Path  string
	Name  string
}

// discoverSegments lists and sorts authoritative segments under dir.
// Only exact segment-%08d.wal names are accepted. Gaps in numbering are reported
// as ErrMissingSegment (e.g. 1 and 3 without 2).
// Empty dir → empty list, nil error.
func discoverSegments(dir string) ([]segmentInfo, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var segs []segmentInfo
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		m := segmentNameRe.FindStringSubmatch(name)
		if m == nil {
			// Ignore non-matching names (backups, junk); do not treat as authoritative.
			continue
		}
		idx, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil || idx == 0 {
			return nil, fmt.Errorf("%w: %s", ErrInvalidSegmentName, name)
		}
		segs = append(segs, segmentInfo{
			Index: idx,
			Path:  filepath.Join(dir, name),
			Name:  name,
		})
	}

	sort.Slice(segs, func(i, j int) bool { return segs[i].Index < segs[j].Index })

	// Detect missing indices in contiguous range from first to last.
	if len(segs) == 0 {
		return segs, nil
	}
	// First segment must be 1 for a complete history starting fresh.
	// Allow starting at 1 only; if first is N>1, missing 1..N-1.
	if segs[0].Index != 1 {
		return nil, fmt.Errorf("%w: expected segment-00000001, found %s", ErrMissingSegment, segs[0].Name)
	}
	for i := 1; i < len(segs); i++ {
		if segs[i].Index == segs[i-1].Index {
			return nil, fmt.Errorf("%w: duplicate index %d", ErrInvalidSegmentName, segs[i].Index)
		}
		if segs[i].Index != segs[i-1].Index+1 {
			return nil, fmt.Errorf("%w: after %s expected index %d, found %s",
				ErrMissingSegment, segs[i-1].Name, segs[i-1].Index+1, segs[i].Name)
		}
	}
	return segs, nil
}
