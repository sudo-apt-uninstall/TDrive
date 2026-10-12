package diffrange

import (
	"fmt"
	"sort"
	"testing"
)

// TestClassifyDisagreements names WHY each disagreement happens, because "157
// differences" is a number that invites either panic or dismissal, and neither
// is warranted. The categories are what a reviewer actually has to sign off.
func TestClassifyDisagreements(t *testing.T) {
	byCategory := map[string]map[string]bool{}
	count := map[string]int{}

	classify := func(size int64, s, c outcome) string {
		switch {
		case size == 0:
			return "A. empty resource: shipping 416s any Range; proposed ignores unparseable ones"
		case s.status == 206 && c.status == 200:
			return "B. '+' sign accepted by shipping, rejected as malformed by proposed"
		case s.status == 416 && c.status == 206:
			return "C. leading/trailing OWS: shipping's HasPrefix fails, proposed trims"
		case s.status == 416 && c.status == 200:
			return "D. unparseable or multi-range: shipping 416s, proposed ignores the header (RFC 9110 14.2)"
		default:
			return "E. UNCLASSIFIED - investigate"
		}
	}

	for _, size := range sizeCorpus() {
		for _, header := range headerCorpus() {
			s := shippingOutcome(header, size)
			c := candidateOutcome(header, size)
			if s == c {
				continue
			}
			key := classify(size, s, c)
			if byCategory[key] == nil {
				byCategory[key] = map[string]bool{}
			}
			byCategory[key][header] = true
			count[key]++
		}
	}

	keys := make([]string, 0, len(byCategory))
	for k := range byCategory {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		headers := make([]string, 0, len(byCategory[k]))
		for h := range byCategory[k] {
			headers = append(headers, fmt.Sprintf("%q", h))
		}
		sort.Strings(headers)
		t.Logf("%s", k)
		t.Logf("    %d cells, %d distinct headers", count[k], len(headers))
		t.Logf("    %v", headers)
	}

	if n := count["E. UNCLASSIFIED - investigate"]; n > 0 {
		t.Fatalf("%d disagreements do not fit any known category", n)
	}
}
