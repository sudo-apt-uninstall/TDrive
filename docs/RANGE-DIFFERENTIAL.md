# The shipping Range parser, measured

**Finding: `parseRangeHeader` in `backend/media/server.go` is not vulnerable to
any of the four defects the nineteen-repository survey found elsewhere.**

This document exists because the opposite was the working assumption, and it
was wrong. `backend/httprange` was written after the survey concluded that no
correct Range implementation existed to copy. The natural next step looked
obvious: find the parser in this repository and replace it.

That step was not taken, because the assumption was tested first.

## What was compared

`backend/diffrange` holds a **verbatim copy** of the shipping function next to
a harness that runs it and `backend/httprange.Parse` over the same inputs, and
reduces both to the HTTP response a client would observe — `200 full`, `206
[start,end]`, or `416`. Return tuples would compare implementation shapes;
outcomes compare behaviour.

```
43 headers x 10 resource sizes = 430 (header, size) pairs
```

Sizes include `0`, `1`, `2`, and values either side of a part boundary
(`511`, `512`, `513`, `1024`).

## The three results that matter

```
$ go test ./backend/diffrange/... -count=1 -v        # CGO_ENABLED=0

--- PASS: TestNeitherParserEverEscapesTheResource
--- PASS: TestProposedNeverServesFewerBytesThanShipping
    no range that the shipping parser satisfies becomes a 416
--- PASS: TestShippingParserIsNotVulnerableToTheSurveyedDefects
```

**1. Neither parser ever emits a `206` naming a byte outside the resource.**
That is the failure that corrupts a download, as opposed to merely annoying a
client, and neither implementation does it anywhere in the input space.

**2. No range the shipping parser satisfies would become a `416`.** Swapping
it could not break playback that works today.

**3. The four surveyed defects are all absent here:**

| Defect found elsewhere | Shipping TDrive parser |
|---|---|
| `bytes=9-3` → length `-5`, used to build headers | refuses it |
| `bytes=1500000-` on a 1,500,000-byte file → clamped to the last byte, answered `206` | refuses it |
| `bytes=-0` treated as satisfiable | refuses it |
| zero-length resource → range handling skipped, `200` with no body | refuses it |
| end past the resource → must be clamped | clamps it, correctly |

The parser is 36 lines and gets every dangerous case right.

## Where the two do disagree

157 of 430 cells, and **every one is classified** — the harness fails if any
disagreement does not fit a known category.

| | Cells | Distinct headers | What it is |
|---|---|---|---|
| **A** | 15 | 15 | **Empty resource.** Shipping answers `416` to *any* `Range` on a zero-byte file. The proposal ignores the ones it cannot parse and serves `200`. |
| **B** | 32 | 4 | **`+` signs.** Shipping accepts `bytes=+5-10`, `bytes=5-+10`, `bytes=+0-`, `bytes=-+5` and serves the range it guesses was meant. The proposal calls them malformed. |
| **C** | 7 | 1 | **Leading whitespace.** `" bytes=5-10"` fails shipping's `HasPrefix` and becomes a `416`. |
| **D** | 103 | 13 | **Unparseable or multi-range.** Shipping answers `416`; RFC 9110 §14.2 says an unsatisfiable-because-unintelligible `Range` should be ignored and the whole representation served. |

Every disagreement is about **how to answer garbage**, not about which bytes a
valid range names. There is no cell where both return `206` and the intervals
differ.

### Category C is latent, not live — measured

It would be easy to call C a bug and move on. It is reachable only if a
`Range` value with leading whitespace can arrive at the handler, and that is a
question about `net/http`, not about this parser. So it was asked directly:

```
--- PASS: TestNetHTTPStripsLeadingWhitespaceFromHeaderValues
    wire value was " bytes=5-10 ", handler receives "bytes=5-10"
```

A real request was parsed with `http.ReadRequest`. `net/http` strips the
optional whitespace before the handler sees it, so **category C cannot occur
through the media server**. It stays a robustness gap worth closing if the
parser is ever called from somewhere that does not trim — and it is now
recorded as a test rather than as a belief.

## The decision

**The parser is not being replaced right now, and the reason is the measurement
rather than caution.**

The case for replacing it is RFC conformance on categories A and D, and
dropping the `+`-sign guesswork in B. The case against is that the shipping
parser is correct everywhere it matters, and the change would alter the
responses asserted by `backend/media/server_test.go` (20,032 bytes), which
cannot be compiled or run in the environment this branch is being built in —
it needs Wails v3, gotd/td and native dependencies.

Changing observable HTTP behaviour while unable to run the tests that describe
it is how a correct parser becomes a broken server. The harness is committed so
that the swap is a twenty-minute job for whoever has a full checkout, with the
exact list of what will change already written down.

## The drift guard

Copying production code into a test package buys a comparison and costs a
liability: the copy can fall out of date, and the report would keep passing
while describing code that no longer runs.

```
--- PASS: TestTheCopyStillMatchesTheOriginal
    copy matches ../media/server.go byte for byte (964 bytes of body)
```

It **fails closed** — a missing original is a failure, not a skip — and it was
inverse-tested in all three states rather than trusted because it was green:

```
1. unmodified          -> ok      (guard passes)
2. original mutated    -> FAIL    "the copy has drifted from ../media/server.go"
3. original deleted    -> FAIL    "cannot read ../media/server.go"
```

State 3 is the one that matters. A guard that quietly skips when it cannot find
its subject is the same vacuous-test trap `backend/datadir/chokepoint_test.go`
already documents.

## Rule earned

**A survey of how other people got something wrong is not evidence that you
got it wrong too.** Nineteen repositories mishandled Range, `httprange` was
written in response, and the next move was going to be replacing a function
nobody had examined. Reading it took ten minutes; measuring it took an hour;
it was already correct on every case that could lose data.

This is the seventh claim on this project refuted by measuring it, and the
first one refuted in our own favour.

## Reproducing

`backend/diffrange` depends only on `backend/httprange` and the standard
library:

```sh
export GOROOT=/path/to/go1.27.2 PATH=$GOROOT/bin:$PATH GOPROXY=off
export CGO_ENABLED=0   # net/http pulls in net, which wants cgo for the resolver
go test ./backend/diffrange/... -count=1 -v
```

`CGO_ENABLED=0` is required in a sandbox without C headers; the pure-Go
resolver is irrelevant to parsing a request that never leaves memory.
