package uhp

import _ "embed"

// SchemaJSON is the normative JSON Schema these types mirror, vendored
// verbatim from the specification repository.
//
// It is a string rather than a []byte so that a consumer cannot edit the copy
// this package validates against. Nothing here reads it at run time; it is
// carried so that the claim "these types match the schema" is checkable rather
// than asserted, by this repository's own tests and by anyone else's.
//
// The file is byte-identical to protocol/schema/uhp-2026-08-11.schema.json at
// harnessrouter `08d61ea145d6b78c433f6910547c1e7ee293c948`, which is the commit
// this copy must be re-read against when it is refreshed. The pin moved from
// `1176d9a5` on 2026-09-01 by re-reading rather than by assumption: the two
// revisions differ only in the conformance suite, and the schema files compared
// equal byte for byte (#107).
//
// # It does go stale within a version
//
// This comment used to say the opposite: that a published UHP version is
// immutable in structure, so the copy is replaced when a new version is
// published rather than refreshed. The first half is still true and the
// inference from it was wrong. Immutable in structure means a field may not be
// renamed, retyped or removed — it does not mean nothing is added, and
// `2026-08-11` has since gained two objects it did not describe on the day it
// was published, `SessionShare` and `TurnItem`, plus `metadata.ignored_fields`
// on Response. All three closed gaps this repository reported (harnessrouter
// #42 and #44), so the additions arrived precisely because the copy was being
// read carefully, which is the opposite of the situation the old comment
// imagined.
//
// A vendored schema pinned to a commit and believed to be evergreen is worse
// than one pinned to a commit and known to need re-reading: the tests in this
// package validate against whatever this file says, so a stale copy makes them
// agree with the past. Re-read it when the specification repository moves.
//
//go:embed schema/uhp-2026-08-11.schema.json
var SchemaJSON string
