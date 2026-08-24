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
// the commit named in docs/adr/0002-uhp-package-models-the-protocol.md. A
// published UHP version is immutable in structure, so this copy does not go
// stale within [Version] — it is replaced when a new version is published, not
// refreshed.
//
//go:embed schema/uhp-2026-08-11.schema.json
var SchemaJSON string
