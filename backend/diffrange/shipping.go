// Package diffrange holds the SHIPPING range parser from
// backend/media/server.go, copied verbatim, next to a harness that compares it
// against backend/httprange.
//
// Nothing here is production code. It exists so that "replacing the shipping
// parser is safe" can be a measurement instead of an argument.
package diffrange

import (
	"strconv"
	"strings"
)

// parseRangeHeaderShipping is an EXACT copy of parseRangeHeader from
// backend/media/server.go at blob c3aa3524a011bca592edce4c8fd61ebab1791bf4.
//
// Do not tidy it. Any edit here invalidates the comparison.
func parseRangeHeaderShipping(raw string, size int64) (start, end int64, partial bool, ok bool) {
	if raw == "" {
		return 0, 0, false, true
	}
	if size <= 0 || !strings.HasPrefix(raw, "bytes=") || strings.Contains(raw, ",") {
		return 0, 0, true, false
	}
	spec := strings.TrimSpace(strings.TrimPrefix(raw, "bytes="))
	a, b, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, true, false
	}
	switch {
	case a == "":
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, true, false
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true, true
	case b == "":
		first, err := strconv.ParseInt(a, 10, 64)
		if err != nil || first < 0 || first >= size {
			return 0, 0, true, false
		}
		return first, size - 1, true, true
	default:
		first, err1 := strconv.ParseInt(a, 10, 64)
		last, err2 := strconv.ParseInt(b, 10, 64)
		if err1 != nil || err2 != nil || first < 0 || last < first || first >= size {
			return 0, 0, true, false
		}
		if last >= size {
			last = size - 1
		}
		return first, last, true, true
	}
}
