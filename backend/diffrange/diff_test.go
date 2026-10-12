package diffrange

import (
	"errors"
	"fmt"
	"sort"
	"testing"

	"TDrive/backend/httprange"
)

// Both parsers are reduced to the HTTP response they cause, because that is
// the only thing a client can observe. Comparing return tuples would compare
// implementation shapes; comparing outcomes compares behaviour.
type outcome struct {
	status int   // 200, 206 or 416
	start  int64 // meaningful only for 206
	end    int64 // meaningful only for 206
}

func (o outcome) String() string {
	switch o.status {
	case 200:
		return "200 full"
	case 416:
		return "416"
	default:
		return fmt.Sprintf("206 [%d,%d]", o.start, o.end)
	}
}

func shippingOutcome(raw string, size int64) outcome {
	start, end, partial, ok := parseRangeHeaderShipping(raw, size)
	switch {
	case !ok:
		return outcome{status: 416}
	case !partial:
		return outcome{status: 200}
	default:
		return outcome{status: 206, start: start, end: end}
	}
}

// candidateOutcome applies RFC 9110 §14.2 to httprange's errors: a header that
// cannot be understood is ignored and the whole representation is served,
// while a well-formed but unsatisfiable range is a 416.
func candidateOutcome(raw string, size int64) outcome {
	if raw == "" {
		return outcome{status: 200}
	}
	r, err := httprange.Parse(raw, size)
	switch {
	case errors.Is(err, httprange.ErrMalformed):
		return outcome{status: 200}
	case errors.Is(err, httprange.ErrUnsatisfiable):
		return outcome{status: 416}
	case err != nil:
		return outcome{status: 416}
	default:
		return outcome{status: 206, start: r.Start, end: r.End}
	}
}

func headerCorpus() []string {
	return []string{
		"",
		"bytes=0-0", "bytes=0-", "bytes=-1", "bytes=1-", "bytes=0-1",
		"bytes=5-10", "bytes=10-5", "bytes=0-999999", "bytes=999999-",
		"bytes=-0", "bytes=-5", "bytes=-999999", "bytes=-1000000",
		"bytes=511-512", "bytes=512-512", "bytes=999-1000",
		"bytes=1000-1000", "bytes=1000-", "bytes=1000-1001",
		"bytes=999-", "bytes=999-999",
		// syntax the two treat differently
		"bytes=+5-10", "bytes=5-+10", "bytes=+0-", "bytes=-+5",
		"bytes=abc-def", "bytes=100", "bytes=-", "bytes=",
		"bytes=0-10,20-30", "bytes=0-10, 20-30",
		"items=0-10", "bytes 0-10", "BYTES=0-10",
		"bytes=99999999999999999999-", "bytes=0-99999999999999999999",
		"bytes= 5-10 ", " bytes=5-10",
		"bytes=05-10", "bytes=0005-0010",
		"bytes=-9223372036854775807", "bytes=9223372036854775807-",
	}
}

func sizeCorpus() []int64 {
	return []int64{0, 1, 2, 10, 511, 512, 513, 1000, 1024, 1_000_000}
}

// TestDifferentialAgainstShipping is not a pass/fail gate. It enumerates the
// full input space and reports every disagreement, so the cost of swapping the
// shipping parser is known before it is swapped rather than after.
func TestDifferentialAgainstShipping(t *testing.T) {
	type disagreement struct {
		header   string
		size     int64
		shipping outcome
		proposed outcome
	}

	var diffs []disagreement
	total := 0
	for _, size := range sizeCorpus() {
		for _, header := range headerCorpus() {
			total++
			s := shippingOutcome(header, size)
			c := candidateOutcome(header, size)
			if s != c {
				diffs = append(diffs, disagreement{header, size, s, c})
			}
		}
	}

	// Group by the shape of the disagreement rather than listing every cell,
	// because the interesting number is how many DISTINCT behaviours change.
	kinds := map[string][]disagreement{}
	for _, d := range diffs {
		key := fmt.Sprintf("%s -> %s", d.shipping, d.proposed)
		if d.shipping.status != 206 && d.proposed.status != 206 {
			key = fmt.Sprintf("%d -> %d", d.shipping.status, d.proposed.status)
		}
		kinds[key] = append(kinds[key], d)
	}
	keys := make([]string, 0, len(kinds))
	for k := range kinds {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	t.Logf("compared %d (header, size) pairs; %d disagreements in %d shapes",
		total, len(diffs), len(kinds))
	for _, k := range keys {
		group := kinds[k]
		t.Logf("  %-24s x%d  e.g. %q size=%d",
			k, len(group), group[0].header, group[0].size)
		for i, d := range group {
			if i >= 4 {
				t.Logf("        ... and %d more", len(group)-i)
				break
			}
			t.Logf("        %-32q size=%-8d shipping=%-16s proposed=%s",
				d.header, d.size, d.shipping, d.proposed)
		}
	}
}

// TestNeitherParserEverEscapesTheResource is the assertion that must hold for
// BOTH. A 206 naming a byte outside the resource is the failure that actually
// corrupts a download, as opposed to merely annoying a client.
func TestNeitherParserEverEscapesTheResource(t *testing.T) {
	for _, size := range sizeCorpus() {
		for _, header := range headerCorpus() {
			for name, got := range map[string]outcome{
				"shipping": shippingOutcome(header, size),
				"proposed": candidateOutcome(header, size),
			} {
				if got.status != 206 {
					continue
				}
				if got.start < 0 || got.end < got.start || got.end >= size {
					t.Errorf("%s: %q size=%d produced %s, which is not inside a %d-byte resource",
						name, header, size, got, size)
				}
			}
		}
	}
}

// TestProposedNeverServesFewerBytesThanShipping guards the one regression that
// would be invisible in a player: a range that used to work returning 416.
func TestProposedNeverServesFewerBytesThanShipping(t *testing.T) {
	var regressions int
	for _, size := range sizeCorpus() {
		for _, header := range headerCorpus() {
			s := shippingOutcome(header, size)
			c := candidateOutcome(header, size)
			if s.status == 206 && c.status == 416 {
				t.Errorf("REGRESSION: %q size=%d served %s, now 416", header, size, s)
				regressions++
			}
		}
	}
	if regressions == 0 {
		t.Log("no range that the shipping parser satisfies becomes a 416")
	}
}
