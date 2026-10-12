package query

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"sort"
)

// page token encodes a start index into a sorted stable list (opaque to clients).
// State can change while paging; tokens remain index-based into the *current*
// sorted snapshot at request time (documented consistency: per-request snapshot).

func normalizePageSize(n int) (int, error) {
	if n < 0 {
		return 0, fmt.Errorf("%w: negative page size", ErrInvalidQuery)
	}
	if n == 0 {
		return DefaultPageSize, nil
	}
	if n > MaxPageSize {
		return 0, fmt.Errorf("%w: max %d", ErrPageSizeExceeded, MaxPageSize)
	}
	return n, nil
}

func encodeToken(offset int) string {
	if offset <= 0 {
		return ""
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(offset))
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func decodeToken(tok string) (int, error) {
	if tok == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(b) != 8 {
		return 0, ErrInvalidPageToken
	}
	off := int(binary.BigEndian.Uint64(b))
	if off < 0 {
		return 0, ErrInvalidPageToken
	}
	return off, nil
}

func paginate[T any](items []T, pageSize int, token string) (slice []T, next string, err error) {
	pageSize, err = normalizePageSize(pageSize)
	if err != nil {
		return nil, "", err
	}
	off, err := decodeToken(token)
	if err != nil {
		return nil, "", err
	}
	if off > len(items) {
		off = len(items)
	}
	end := off + pageSize
	if end > len(items) {
		end = len(items)
	}
	slice = items[off:end]
	if end < len(items) {
		next = encodeToken(end)
	}
	return slice, next, nil
}

func sortBySeqDesc[T any](items []T, seq func(T) uint64) {
	sort.SliceStable(items, func(i, j int) bool {
		return seq(items[i]) > seq(items[j])
	})
}
