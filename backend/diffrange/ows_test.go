package diffrange

import (
	"bufio"
	"net/http"
	"strings"
	"testing"
)

// TestNetHTTPStripsLeadingWhitespaceFromHeaderValues decides whether the
// " bytes=5-10" disagreement is a LIVE bug or a LATENT one.
//
// The shipping parser uses strings.HasPrefix(raw, "bytes=") with no trim, so a
// leading space makes it answer 416 to a range it understands. That only
// matters if such a value can reach it. HTTP allows optional whitespace around
// a field value, so the question is whether net/http hands it over or removes
// it first.
//
// Measured with a real request parse rather than asserted from memory.
func TestNetHTTPStripsLeadingWhitespaceFromHeaderValues(t *testing.T) {
	raw := "GET /media/file/tok HTTP/1.1\r\n" +
		"Host: 127.0.0.1\r\n" +
		"Range:  bytes=5-10 \r\n" +
		"\r\n"

	req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	got := req.Header.Get("Range")
	t.Logf("wire value was %q, handler receives %q", " bytes=5-10 ", got)

	if got != "bytes=5-10" {
		t.Fatalf("net/http delivered %q; the OWS assumption in the write-up is wrong", got)
	}

	// Therefore the shipping parser never sees the untrimmed form through a
	// net/http server, and the disagreement is latent. Record the reasoning
	// where it can be re-run, not in a comment somewhere else.
	start, end, partial, ok := parseRangeHeaderShipping(got, 1000)
	if !ok || !partial || start != 5 || end != 10 {
		t.Fatalf("shipping parser on the delivered value: (%d,%d,%v,%v), want (5,10,true,true)",
			start, end, partial, ok)
	}
}

// TestShippingParserIsNotVulnerableToTheSurveyedDefects is the inverse of the
// audit's working assumption.
//
// The nineteen-repo survey found negative lengths, out-of-range clamps and
// skipped empty-file handling. It would be easy to assume TDrive inherited
// them. It did not, and that is worth asserting so nobody "fixes" a parser
// that is already correct on the dangerous cases.
func TestShippingParserIsNotVulnerableToTheSurveyedDefects(t *testing.T) {
	const size = 1_500_000

	// bytes=9-3: Sam8r computed length -5 and built headers from it.
	if _, _, _, ok := parseRangeHeaderShipping("bytes=9-3", size); ok {
		t.Error("inverted range accepted")
	}
	// bytes=1500000-: Sam8r clamped to the last byte and answered 206.
	if _, _, _, ok := parseRangeHeaderShipping("bytes=1500000-", size); ok {
		t.Error("start past end accepted")
	}
	// bytes=-0: a zero-length suffix must not be satisfiable.
	if _, _, _, ok := parseRangeHeaderShipping("bytes=-0", size); ok {
		t.Error("zero-length suffix accepted")
	}
	// Zero-length resource: must not fall through to a whole-file branch.
	if _, _, partial, ok := parseRangeHeaderShipping("bytes=0-0", 0); ok || !partial {
		t.Error("range against an empty resource was not refused")
	}
	// An end past the resource IS clamped, which is required.
	start, end, partial, ok := parseRangeHeaderShipping("bytes=1499998-99999999", size)
	if !ok || !partial || start != 1_499_998 || end != 1_499_999 {
		t.Errorf("end clamp: (%d,%d,%v,%v), want (1499998,1499999,true,true)",
			start, end, partial, ok)
	}
}
