package diffrange

import (
	"os"
	"strings"
	"testing"
)

// TestTheCopyStillMatchesTheOriginal is the price of copying production code
// into a test package.
//
// A verbatim copy is only evidence while it stays verbatim. The moment
// backend/media/server.go changes and this file does not, every result in the
// differential report becomes a statement about code that no longer runs --
// and it would keep passing, silently, which is the worst available outcome.
//
// This test fails closed: a missing or unreadable original is a failure, not a
// skip. A guard that disables itself when it cannot find its subject is the
// same vacuous-test trap the chokepoint walk already documents.
func TestTheCopyStillMatchesTheOriginal(t *testing.T) {
	const originalPath = "../media/server.go"

	source, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("cannot read %s: %v\n"+
			"This test compares a copied function against its original and has "+
			"no value without it. Do not convert this into a skip.", originalPath, err)
	}

	original, err := extractFunc(string(source), "func parseRangeHeader(")
	if err != nil {
		t.Fatalf("in %s: %v", originalPath, err)
	}

	copySource, err := os.ReadFile("shipping.go")
	if err != nil {
		t.Fatalf("cannot read shipping.go: %v", err)
	}
	copied, err := extractFunc(string(copySource), "func parseRangeHeaderShipping(")
	if err != nil {
		t.Fatalf("in shipping.go: %v", err)
	}

	if original != copied {
		t.Fatalf("the copy has drifted from %s\n\n"+
			"--- original body ---\n%s\n--- copied body ---\n%s\n\n"+
			"Re-copy the function verbatim and re-run the differential report. "+
			"Every finding in docs/RANGE-DIFFERENTIAL.md is stale until you do.",
			originalPath, original, copied)
	}
	t.Logf("copy matches %s byte for byte (%d bytes of body)", originalPath, len(copied))
}

// extractFunc returns everything between the first "{" of the named function
// and the closing brace that sits in column zero. Signatures are excluded, so
// the two functions may differ in name and nothing else.
func extractFunc(source, signature string) (string, error) {
	start := strings.Index(source, signature)
	if start < 0 {
		return "", &notFoundError{signature}
	}
	open := strings.Index(source[start:], "{")
	if open < 0 {
		return "", &notFoundError{signature + " opening brace"}
	}
	body := source[start+open+1:]
	end := strings.Index(body, "\n}")
	if end < 0 {
		return "", &notFoundError{signature + " closing brace"}
	}
	return strings.TrimSpace(body[:end]), nil
}

type notFoundError struct{ what string }

func (e *notFoundError) Error() string { return "could not locate " + e.what }
