package uhp

// File is the protocol's file object, in either direction: a file a client
// uploaded before a task, and a file a run produced.
//
// # Ids carry no path
//
// ID is opaque, and that is a security property rather than a style choice. An
// id derived from — or worse, equal to — a path makes every download handler a
// path-joining exercise against attacker-influenced input, which Files §5 names
// as the single most likely serious vulnerability in a UHP server. A client
// should treat this id as a token to hand back, never as a location to
// manipulate.
//
// Filename may still carry a path within the container, and this is not a
// contradiction: two files called report.md in different directories are
// different files, and a client shown only the base name could not tell them
// apart. It is a label to display, and ID is what resolves.
type File struct {
	ID string `json:"id"`
	// Object is always "file".
	//
	// Omitted when unset rather than written as an empty string, for the reason
	// [Session].Object is: the schema pins it to a constant, and this type has
	// no MarshalJSON to default it in.
	Object string `json:"object,omitempty"`
	// ContainerID names the session's file store. A session and its container
	// are the same thing named from two chapters, so one id derives from the
	// other.
	ContainerID string `json:"container_id,omitempty"`
	Filename    string `json:"filename"`
	Bytes       int64  `json:"bytes"`
	// CreatedAt is Unix seconds.
	CreatedAt int64 `json:"created_at"`
}
