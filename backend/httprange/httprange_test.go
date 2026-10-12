package httprange

import (
	"errors"
	"testing"
)

// Every case below that is marked with a repository name is one a surveyed
// implementation got wrong. They are the reason this package exists, so they
// are the first thing it is tested against.
func TestParse(t *testing.T) {
	const size = 1_500_000

	cases := []struct {
		name   string
		header string
		size   int64
		want   Range
		err    error
	}{
		// --- the ordinary cases, which everyone gets right ---
		{name: "whole file explicit", header: "bytes=0-1499999", size: size,
			want: Range{Start: 0, End: 1_499_999, Length: size}},
		{name: "open from zero", header: "bytes=0-", size: size,
			want: Range{Start: 0, End: 1_499_999, Length: size}},
		{name: "middle", header: "bytes=100-199", size: size,
			want: Range{Start: 100, End: 199, Length: 100}},

		// --- single byte: the inclusive-end trap ---
		// "bytes=0-0" is ONE byte. An implementation treating End as
		// exclusive returns zero bytes here and is off by one everywhere.
		{name: "first byte only", header: "bytes=0-0", size: size,
			want: Range{Start: 0, End: 0, Length: 1}},
		{name: "last byte only", header: "bytes=1499999-1499999", size: size,
			want: Range{Start: 1_499_999, End: 1_499_999, Length: 1}},

		// --- suffix form ---
		{name: "suffix one byte", header: "bytes=-1", size: size,
			want: Range{Start: 1_499_999, End: 1_499_999, Length: 1}},
		{name: "suffix many", header: "bytes=-500", size: size,
			want: Range{Start: 1_499_500, End: 1_499_999, Length: 500}},
		// A suffix longer than the file is satisfiable and means all of it.
		{name: "suffix longer than file", header: "bytes=-99999999", size: size,
			want: Range{Start: 0, End: 1_499_999, Length: size}},
		{name: "suffix exactly the file", header: "bytes=-1500000", size: size,
			want: Range{Start: 0, End: 1_499_999, Length: size}},
		// RFC 9110 §14.1.2: a zero-length suffix is unsatisfiable.
		{name: "suffix zero", header: "bytes=-0", size: size, err: ErrUnsatisfiable},

		// --- the failures found in the survey ---
		// Sam8r/TGWebDrive computed length -5 for this and built headers from
		// it instead of refusing.
		{name: "inverted range", header: "bytes=9-3", size: size, err: ErrUnsatisfiable},
		// Sam8r clamped this to the last byte, returning data that was not
		// requested under a 206.
		{name: "start past end, open", header: "bytes=1500000-", size: size, err: ErrUnsatisfiable},
		{name: "start past end, closed", header: "bytes=1500000-1500005", size: size, err: ErrUnsatisfiable},
		{name: "start far past end", header: "bytes=99999999-", size: size, err: ErrUnsatisfiable},
		// An end past the resource IS clamped - that part is required.
		{name: "end past file is truncated", header: "bytes=1499998-99999999", size: size,
			want: Range{Start: 1_499_998, End: 1_499_999, Length: 2}},

		// --- zero-length resources ---
		// Sam8r bypassed range parsing entirely for these and served its
		// whole-file branch: a 200 with no body, to a client that asked for a
		// specific byte.
		{name: "empty file, closed", header: "bytes=0-0", size: 0, err: ErrUnsatisfiable},
		{name: "empty file, open", header: "bytes=0-", size: 0, err: ErrUnsatisfiable},
		{name: "empty file, suffix", header: "bytes=-1", size: 0, err: ErrUnsatisfiable},

		// --- malformed: ignore the header, serve 200 ---
		{name: "not bytes unit", header: "items=0-10", size: size, err: ErrMalformed},
		{name: "no dash", header: "bytes=100", size: size, err: ErrMalformed},
		{name: "neither bound", header: "bytes=-", size: size, err: ErrMalformed},
		{name: "not a number", header: "bytes=abc-def", size: size, err: ErrMalformed},
		// A negative start must not survive parsing. strconv.ParseInt would
		// have accepted the sign and let it through every later comparison.
		{name: "negative start", header: "bytes=-5-10", size: size, err: ErrMalformed},
		{name: "signed start", header: "bytes=+5-10", size: size, err: ErrMalformed},
		{name: "empty spec", header: "bytes=", size: size, err: ErrMalformed},
		{name: "multiple ranges", header: "bytes=0-10,20-30", size: size, err: ErrMalformed},
		// 20 digits overflows int64.
		{name: "overflow", header: "bytes=99999999999999999999-", size: size, err: ErrMalformed},
		{name: "whitespace tolerated", header: " bytes=100-199 ", size: size,
			want: Range{Start: 100, End: 199, Length: 100}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.header, tc.size)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("Parse(%q, %d) error = %v, want %v", tc.header, tc.size, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q, %d) unexpected error: %v", tc.header, tc.size, err)
			}
			if got != tc.want {
				t.Fatalf("Parse(%q, %d) = %+v, want %+v", tc.header, tc.size, got, tc.want)
			}
		})
	}
}

// TestLengthAlwaysAgreesWithTheInterval is the invariant that the surveyed
// implementations broke. Length is stored, so it can drift from the ends it
// was derived from; nothing else in the package is safe if it does.
func TestLengthAlwaysAgreesWithTheInterval(t *testing.T) {
	sizes := []int64{1, 2, 511, 512, 513, 1023, 1024, 1025, 1_500_000}
	headers := []string{
		"bytes=0-0", "bytes=0-", "bytes=-1", "bytes=-2", "bytes=1-",
		"bytes=0-1", "bytes=511-512", "bytes=1023-1024", "bytes=-1000000",
	}
	for _, size := range sizes {
		for _, header := range headers {
			r, err := Parse(header, size)
			if err != nil {
				continue // unsatisfiable or malformed for this size; fine
			}
			if r.Length != r.End-r.Start+1 {
				t.Fatalf("Parse(%q, %d) = %+v: Length %d disagrees with End-Start+1 = %d",
					header, size, r, r.Length, r.End-r.Start+1)
			}
			if r.Start < 0 || r.End >= size || r.Start > r.End {
				t.Fatalf("Parse(%q, %d) = %+v: interval escapes a %d-byte resource",
					header, size, r, size)
			}
		}
	}
}

// TestSplitCoversExactlyTheRange is the differential test.
//
// It reconstructs the byte interval from the part plan and requires it to be
// the interval that went in. The over-fetching implementation in the survey
// would fail this immediately: its part plan covered 1,048,575 bytes for a
// one-byte range.
func TestSplitCoversExactlyTheRange(t *testing.T) {
	const partSize = 512 * 1024
	const size = 10_000_000

	headers := []string{
		"bytes=0-0",
		"bytes=0-",
		"bytes=-1",
		"bytes=-524288",
		"bytes=524287-524288", // spans a part boundary by one byte
		"bytes=524288-524288", // first byte of the second part
		"bytes=524287-524287", // last byte of the first part
		"bytes=0-524287",      // exactly one part
		"bytes=0-524288",      // one part plus one byte
		"bytes=9999999-",      // last byte of the file
		"bytes=12345-67890",
	}

	for _, header := range headers {
		t.Run(header, func(t *testing.T) {
			r, err := Parse(header, size)
			if err != nil {
				t.Fatalf("Parse(%q): %v", header, err)
			}
			parts, err := Split(r, partSize)
			if err != nil {
				t.Fatalf("Split(%+v): %v", r, err)
			}

			// Rebuild the interval the plan describes.
			gotStart := int64(parts.First)*partSize + parts.FirstTrim
			gotEnd := int64(parts.Last)*partSize + parts.LastKeep - 1
			if gotStart != r.Start || gotEnd != r.End {
				t.Fatalf("plan %+v rebuilds [%d,%d], want [%d,%d]",
					parts, gotStart, gotEnd, r.Start, r.End)
			}

			// Every part in the plan must contribute at least one byte.
			//
			// This is the assertion that matters, and it was missing from the
			// first version of this file. A plan that appends a part
			// contributing nothing still rebuilds the correct interval,
			// because LastKeep == 0 cancels the extra part exactly. Mutation
			// testing caught it: seeding `last := (r.Start + r.Length)/partSize`
			// -- the FileToLink shape -- survived the whole suite.
			if parts.LastKeep <= 0 {
				t.Fatalf("plan %+v ends on a part that contributes %d bytes; "+
					"that part should not be in the plan",
					parts, parts.LastKeep)
			}
			if parts.LastKeep > partSize {
				t.Fatalf("plan %+v keeps %d bytes of a %d-byte part",
					parts, parts.LastKeep, partSize)
			}
			if parts.FirstTrim < 0 || parts.FirstTrim >= partSize {
				t.Fatalf("plan %+v trims %d bytes off a %d-byte part",
					parts, parts.FirstTrim, partSize)
			}

			// The part count must be the minimum that can cover the interval.
			// Computed here by counting distinct part indices directly, not by
			// reusing the formula under test.
			want := r.End/partSize - r.Start/partSize + 1
			if int64(parts.Count()) != want {
				t.Fatalf("plan %+v uses %d parts for [%d,%d], want %d",
					parts, parts.Count(), r.Start, r.End, want)
			}

			// Over-fetch must be bounded by one part at each end. More than
			// that means the ends were computed from different quantities.
			fetched := parts.Fetched(partSize)
			if waste := fetched - r.Length; waste >= 2*partSize {
				t.Fatalf("plan %+v fetches %d bytes for a %d-byte range: %d wasted, over two parts",
					parts, fetched, r.Length, waste)
			}
		})
	}
}

// TestSplitSingleByteFetchesOnePart is the specific regression.
//
// prgofficial/FileToLink emitted 1,048,575 bytes for this request, because
// first_part came from the offset and last_part came from the limit.
func TestSplitSingleByteFetchesOnePart(t *testing.T) {
	const partSize = 512 * 1024
	r, err := Parse("bytes=0-0", 1_500_000)
	if err != nil {
		t.Fatal(err)
	}
	parts, err := Split(r, partSize)
	if err != nil {
		t.Fatal(err)
	}
	if parts.Count() != 1 {
		t.Fatalf("one-byte range needs %d parts, want 1 (plan %+v)", parts.Count(), parts)
	}
	if got := parts.Fetched(partSize); got != partSize {
		t.Fatalf("one-byte range fetches %d bytes, want %d", got, partSize)
	}
	if parts.LastKeep != 1 {
		t.Fatalf("LastKeep = %d, want 1", parts.LastKeep)
	}

	// The same request one byte before a part boundary. bytes=0-0 happens to
	// be the single offset where the buggy formula agrees with the correct
	// one, so testing only that offset proves nothing. Here the two diverge:
	// the buggy plan asks for two parts and keeps zero bytes of the second.
	r, err = Parse("bytes=524287-524287", 1_500_000)
	if err != nil {
		t.Fatal(err)
	}
	parts, err = Split(r, partSize)
	if err != nil {
		t.Fatal(err)
	}
	if parts.Count() != 1 {
		t.Fatalf("one-byte range at a part boundary needs %d parts, want 1 (plan %+v)",
			parts.Count(), parts)
	}
	if parts.First != 0 || parts.Last != 0 {
		t.Fatalf("plan %+v, want part 0 only", parts)
	}
	if got := parts.Fetched(partSize); got != partSize {
		t.Fatalf("fetches %d bytes for one byte, want %d", got, partSize)
	}
}

func TestSplitRejectsNonsense(t *testing.T) {
	if _, err := Split(Range{Start: 0, End: 0, Length: 1}, 0); !errors.Is(err, ErrMalformed) {
		t.Fatalf("zero part size: err = %v, want ErrMalformed", err)
	}
	if _, err := Split(Range{Start: 0, End: 0, Length: 1}, -1); !errors.Is(err, ErrMalformed) {
		t.Fatalf("negative part size: err = %v, want ErrMalformed", err)
	}
	if _, err := Split(Range{Start: 10, End: 3, Length: -6}, 512); !errors.Is(err, ErrMalformed) {
		t.Fatalf("inverted interval: err = %v, want ErrMalformed", err)
	}
}

func TestContentRangeHeaders(t *testing.T) {
	r := Range{Start: 100, End: 199, Length: 100}
	if got, want := r.ContentRange(1_500_000), "bytes 100-199/1500000"; got != want {
		t.Fatalf("ContentRange = %q, want %q", got, want)
	}
	if got, want := UnsatisfiedContentRange(1_500_000), "bytes */1500000"; got != want {
		t.Fatalf("UnsatisfiedContentRange = %q, want %q", got, want)
	}
}
