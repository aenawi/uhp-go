// Package uhp is the wire vocabulary of the Unified Harness Protocol, version
// 2026-08-11.
//
// It models the protocol, not the server that ships alongside it. Every one of
// the twenty-three objects in uhp-2026-08-11.schema.json has a type here, with
// every property the schema gives it, including properties this repository's
// server ignores — a client author reading godoc is entitled to the protocol,
// not to one implementation's subset of it. Where this server is narrower than
// the specification, the shortfall is recorded in docs/conformance.md rather
// than hidden by leaving a field out.
//
// # The schema's names win
//
// The types are named as the schema names them: [Response], not "Task";
// [File], not "Artifact"; [SessionList], not "SessionPage". The words this
// repository uses internally are its own and are recorded in CONTEXT.md; they
// have no standing here.
//
// # This package is the protocol, uhpgo is not
//
// Every object in the schema is additionalProperties: true, so a server may add
// fields — and this one does. Those additions live in the subpackage
// [github.com/aenawi/uhp-go/uhp/uhpgo], never here, so that the boundary
// between "the protocol says so" and "this server does so" is a fact the
// compiler can see rather than a claim in a comment.
//
// # Use keyed struct literals
//
// UHP permits a server to add response fields within a published version, and
// this package will follow when one is added. Go permits adding a struct field
// too — but an unkeyed composite literal, uhp.Response{a, b, c}, stops
// compiling the moment one appears. Write uhp.Response{ID: …} and an addition
// costs you nothing. There is no other mitigation available, which is why this
// is stated rather than merely implied.
//
// # Versions are dates
//
// UHP versions are dates, not semantic versions, and a published version is
// immutable in structure: within 2026-08-11 a server may add fields, event
// types and vendor-prefixed error codes, and may not rename a field, change its
// type or meaning, or remove an event type. These shapes were therefore frozen
// on the day the version was published. See docs/adr/0002-uhp-package-models-the-protocol.md.
//
// A conformant client must ignore unknown fields, unknown event types and
// unknown output-item types, and must treat an unrecognised error code as its
// [Error].Type. Nothing in this package will break those rules on your behalf,
// and [EventDecoder] is built so that following the second one is the path of
// least resistance rather than a thing you have to remember.
package uhp

// Version is the protocol version these types describe.
//
// It is a date because UHP versions are dates. It is not this package's version
// and it is not the server's; it names the specification the shapes below were
// mirrored from, and it is what belongs in a UHP-Version header.
const Version = "2026-08-11"
