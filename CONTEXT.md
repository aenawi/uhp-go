# uhp-go

A server implementing the Unified Harness Protocol (UHP): one HTTP contract that drives
several agent harnesses. The protocol is specified externally at
unifiedharnessprotocol.org; this context covers the words this implementation uses.

## Wire and internal names

UHP names the objects it puts on the wire. This project keeps its own word for the
internal thing whenever the internal thing is genuinely different — a unit of work is not
the same as the object describing it, and a file a run produced is not the same as any
file. Where both words exist, the wire word is the protocol's and is not ours to change.

**Response**:
The wire object UHP returns for a unit of work. Its shape is fixed by the protocol schema
for the version being served.
_Avoid_: Task (that is the internal word), Result, Completion

**Task**:
The internal unit of work: an input, the harness that runs it, and the run's bookkeeping.
Carries the Response it will be reported as.
_Avoid_: Job, Run, Request

**File**:
The wire object for a file, in either direction.
_Avoid_: Attachment, Document, Blob

**Artifact**:
A file produced by a run, discovered by walking the session's working directory. Every
Artifact is reported as a File; not every File is an Artifact.
_Avoid_: Output file, Result file

**SessionList** / **SessionPage**:
The wire object for one page of a session listing, and its internal counterpart.
_Avoid_: Sessions response, Page of sessions

## Core concepts

**Harness**:
One configured runtime backend — the thing that turns a model into a working agent by
planning, calling tools, and iterating. Named by an opaque `chrn_` id and a `base`.
_Avoid_: Backend, Adapter, Agent, Provider

**Base**:
Which runtime a Harness runs, as a string the protocol deliberately does not enumerate.
_Avoid_: Type, Kind, Engine

**Session**:
A continued conversation across several Tasks, preserving conversational context, the
working directory, and the configured Harness.
_Avoid_: Conversation, Thread, Trace

**Turn**:
One Task in a Session's history, carrying enough to rebuild a transcript.
_Avoid_: Message, Exchange, Step

**Container**:
A Session's file store, seen from the Files chapter. A Session and its Container are the
same thing named from two places, so one id derives from the other.
_Avoid_: Workspace, Bucket, Volume

**Capability**:
Something a Harness or the server advertises before a client relies on it. Advertised
capabilities are enforced, not merely reported.
_Avoid_: Feature, Flag, Support

**Conformance class**:
What the conformance suite grades an implementation into: `core`, `extended`, or `full`.
A claim, falsifiable by running the suite.
_Avoid_: Level, Tier, Compliance level

**Protocol version**:
A date, `YYYY-MM-DD`, naming a published version of UHP. Immutable in structure once
published: within a version, fields may be added but never renamed, removed, or
redefined.
_Avoid_: API version, Semver, Release
