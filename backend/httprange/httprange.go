// Package httprange parses a single HTTP Range header into a byte interval,
// and maps that interval onto fixed-size transfer parts.
//
// It exists because a nineteen-repository survey of Telegram-backed drives
// found no correct implementation to copy. The two closest candidates both
// failed on the same shape of input:
//
//   - One derived the first part from the range's *offset* and the last part
//     from its *length*, so the two ends of a single interval were computed in
//     different coordinate systems. A one-byte request emitted 1,048,575 bytes.
//
//   - The other computed a negative length for "bytes=9-3" and went on to
//     build headers from it rather than rejecting the range, clamped
//     "bytes=1500000-" past the end of the file instead of refusing it, and
//     skipped range parsing entirely for a zero-length file.
//
// Both were described in their own repositories as correct. Neither was
// tested at a boundary.
//
// The rules implemented here are RFC 9110 §14.1.1 and §14.4. The one that
// causes most of the trouble is that a range is *inclusive at both ends*:
// "bytes=0-0" is one byte, not zero. Treating End as exclusive is the single
// most common way to be off by one in both directions at once.
package httprange

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrUnsatisfiable means the header was well-formed but cannot be satisfied
// for a resource of this size. The caller must answer 416 and send a
// Content-Range of "bytes */<size>".
//
// It is deliberately distinct from ErrMalformed. A client that asks for byte
// 1,500,000 of a 1,000,000-byte file has made a different mistake from one
// that sends "bytes=abc", and RFC 9110 gives them different answers: the
// first is 416, the second is an invalid header a server should ignore while
// serving the whole representation.
var ErrUnsatisfiable = errors.New("httprange: range not satisfiable")

// ErrMalformed means the header could not be understood at all. Per RFC 9110
// §14.2 the correct response is to ignore the header and return the whole
// representation with 200, not to fail the request.
var ErrMalformed = errors.New("httprange: malformed range header")

// Range is a resolved, inclusive byte interval.
//
// Both ends are absolute offsets into the resource, and Start <= End always
// holds for a value returned by Parse. Length is stored rather than derived at
// each use, because recomputing End-Start+1 at three call sites is how the
// surveyed implementations ended up disagreeing with themselves.
type Range struct {
	Start  int64
	End    int64 // inclusive
	Length int64
}

// ContentRange renders the Content-Range header value for this range.
func (r Range) ContentRange(size int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", r.Start, r.End, size)
}

// UnsatisfiedContentRange renders the Content-Range value that must accompany
// a 416 response. RFC 9110 §14.4 requires the unsatisfied form, which carries
// the size and no interval.
func UnsatisfiedContentRange(size int64) string {
	return fmt.Sprintf("bytes */%d", size)
}

// Parse resolves a Range header against a known resource size.
//
// size must be the exact size of the resource. A size of 0 is legal and every
// range against it is unsatisfiable, which is worth stating out loud: one
// surveyed implementation skipped range handling for empty resources and fell
// through to its whole-file branch, answering 200 with no body to a client
// that had asked for a specific byte.
func Parse(header string, size int64) (Range, error) {
	if size < 0 {
		return Range{}, fmt.Errorf("%w: negative resource size %d", ErrMalformed, size)
	}

	spec, ok := strings.CutPrefix(strings.TrimSpace(header), "bytes=")
	if !ok {
		// Other units are legal to send and legal to ignore. Ignoring is the
		// specified behaviour, so this is malformed-for-our-purposes rather
		// than unsatisfiable.
		return Range{}, fmt.Errorf("%w: only the bytes unit is supported: %q", ErrMalformed, header)
	}
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Range{}, fmt.Errorf("%w: empty range specification", ErrMalformed)
	}
	// Multiple ranges are valid HTTP and would require a multipart/byteranges
	// response. Refusing them explicitly is honest; silently serving the first
	// would answer a question the client did not ask.
	//
	// This check is deliberately redundant and is a known mutation survivor:
	// deleting it does not fail any test, because parseUint rejects the comma
	// that ends up inside one of the bounds. Keep it anyway. It states the
	// intent at the point the decision is made, and it does not depend on
	// parseUint staying strict. If both were ever loosened, a multi-range
	// request would be answered 206 carrying only the first range's bytes
	// under a Content-Range describing only that range -- a wrong body that
	// looks correct to the client.
	if strings.Contains(spec, ",") {
		return Range{}, fmt.Errorf("%w: multiple ranges are not supported", ErrMalformed)
	}

	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return Range{}, fmt.Errorf("%w: missing '-' in %q", ErrMalformed, spec)
	}
	firstText := strings.TrimSpace(spec[:dash])
	lastText := strings.TrimSpace(spec[dash+1:])

	switch {
	case firstText == "" && lastText == "":
		return Range{}, fmt.Errorf("%w: %q has neither bound", ErrMalformed, spec)

	case firstText == "":
		// Suffix form: bytes=-N, the final N bytes.
		n, err := parseUint(lastText)
		if err != nil {
			return Range{}, err
		}
		// "bytes=-0" asks for the last zero bytes. Well-formed, and
		// unsatisfiable by RFC 9110 §14.1.2.
		if n == 0 {
			return Range{}, fmt.Errorf("%w: suffix length 0", ErrUnsatisfiable)
		}
		if size == 0 {
			return Range{}, fmt.Errorf("%w: resource is empty", ErrUnsatisfiable)
		}
		// A suffix longer than the resource is satisfiable and means the whole
		// resource. This clamp *is* correct, and it is worth contrasting with
		// the one below that is not: clamping a suffix narrows the answer to
		// what exists, while clamping an out-of-range start invents a byte the
		// client never asked for.
		if n > size {
			n = size
		}
		start := size - n
		return Range{Start: start, End: size - 1, Length: n}, nil

	case lastText == "":
		// Open form: bytes=N-, from N to the end.
		start, err := parseUint(firstText)
		if err != nil {
			return Range{}, err
		}
		if size == 0 || start >= size {
			// Not a clamp. A start at or past the end has no bytes behind it,
			// and answering with the last byte instead — as one surveyed
			// implementation does — returns data the client did not request,
			// under a success status.
			return Range{}, fmt.Errorf("%w: start %d is past the last byte of a %d-byte resource", ErrUnsatisfiable, start, size)
		}
		return Range{Start: start, End: size - 1, Length: size - start}, nil

	default:
		start, err := parseUint(firstText)
		if err != nil {
			return Range{}, err
		}
		end, err := parseUint(lastText)
		if err != nil {
			return Range{}, err
		}
		if end < start {
			// "bytes=9-3". The arithmetic gives a length of -5, and a negative
			// length propagates into Content-Length and into buffer sizing.
			// Reject it here, while it is still a parsing question.
			return Range{}, fmt.Errorf("%w: last byte %d precedes first byte %d", ErrUnsatisfiable, end, start)
		}
		if size == 0 || start >= size {
			return Range{}, fmt.Errorf("%w: start %d is past the last byte of a %d-byte resource", ErrUnsatisfiable, start, size)
		}
		// Truncating an end past the resource is required, not optional: RFC
		// 9110 takes last-byte-pos as size-1 when it is larger. The start is
		// already proven to be inside the resource, so this cannot produce an
		// inverted interval.
		if end > size-1 {
			end = size - 1
		}
		return Range{Start: start, End: end, Length: end - start + 1}, nil
	}
}

// parseUint accepts only a non-negative decimal integer.
//
// strconv.ParseInt would accept a leading "+" or "-", and a negative start
// that survived parsing would sail through every later comparison. The digit
// check is what makes the type signature honest.
func parseUint(text string) (int64, error) {
	if text == "" {
		return 0, fmt.Errorf("%w: empty number", ErrMalformed)
	}
	for _, c := range text {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%w: %q is not a decimal number", ErrMalformed, text)
		}
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		// Overflow. A 20-digit number is syntactically a number and
		// semantically unanswerable; treating it as malformed means the
		// response is the whole resource rather than a confusing 416.
		return 0, fmt.Errorf("%w: %q does not fit in an int64", ErrMalformed, text)
	}
	return value, nil
}

// Parts is the set of fixed-size transfer parts a Range intersects.
//
// Telegram serves files in fixed-size parts, so a byte interval has to become
// a part interval before anything can be fetched. Both ends are inclusive
// part indices.
type Parts struct {
	First int // inclusive
	Last  int // inclusive
	// FirstTrim is how many bytes to discard from the front of the first part.
	FirstTrim int64
	// LastKeep is how many bytes of the last part belong to the range.
	LastKeep int64
}

// Count is the number of parts that must be fetched.
func (p Parts) Count() int { return p.Last - p.First + 1 }

// Fetched is the total bytes transferred to satisfy the range, including the
// portions of the first and last blocks that are discarded.
func (p Parts) Fetched(partSize int64) int64 {
	return int64(p.Count()) * partSize
}

// Split maps a resolved Range onto parts of partSize bytes.
//
// Both ends are derived from absolute offsets — Start and End — and never from
// Length. That is the entire lesson of the survey: the implementation that
// over-fetched a megabyte for one byte computed its first part from an offset
// and its last part from a length, and the two disagreed for every range that
// did not begin at zero.
func Split(r Range, partSize int64) (Parts, error) {
	if partSize <= 0 {
		return Parts{}, fmt.Errorf("%w: part size %d must be positive", ErrMalformed, partSize)
	}
	if r.Length <= 0 || r.End < r.Start {
		return Parts{}, fmt.Errorf("%w: cannot split an empty interval", ErrMalformed)
	}

	first := r.Start / partSize
	last := r.End / partSize

	return Parts{
		First:     int(first),
		Last:      int(last),
		FirstTrim: r.Start - first*partSize,
		LastKeep:  r.End - last*partSize + 1,
	}, nil
}
