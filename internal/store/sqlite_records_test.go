package store

import (
	"reflect"
	"testing"

	"github.com/aenawi/uhp-go/internal/domain"
)

// encodeTask and encodeSession copy field by field, so a field added to
// domain.Task or domain.Session and not added here is data this engine
// silently drops — no compiler error, no failing round-trip test unless
// someone remembers to extend the fixture.
//
// This is the compiler signal that is otherwise missing. It compares field
// names rather than counts, so a rename is caught as well as an addition, and
// it fails at the place that has to change rather than in whichever endpoint
// happened to need the field.
func TestSQLiteRecordsCoverDomainFields(t *testing.T) {
	tests := []struct {
		name       string
		domainType any
		recordType any
		// atLeast is the field count below which the comparison would be
		// vacuously true — a reflect walk that returned nothing would pass
		// both loops and assert that no field exists rather than that every
		// field is covered.
		atLeast int
	}{
		{name: "task", domainType: domain.Task{}, recordType: taskRecord{}, atLeast: 20},
		{name: "session", domainType: domain.Session{}, recordType: sessionRecord{}, atLeast: 9},
		{name: "artifact", domainType: domain.Artifact{}, recordType: artifactRecord{}, atLeast: 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := fieldNames(tc.domainType)
			got := fieldNames(tc.recordType)
			if len(want) < tc.atLeast {
				t.Fatalf("%T has %d fields, fewer than the %d this check was written against — "+
					"either a field was removed on purpose and this number should follow it, "+
					"or the comparison below is passing vacuously",
					tc.domainType, len(want), tc.atLeast)
			}
			for name := range want {
				if !got[name] {
					t.Errorf("%T has field %s and %T does not — storing a %[1]T would drop it",
						tc.domainType, name, tc.recordType)
				}
			}
			for name := range got {
				if !want[name] {
					t.Errorf("%T has field %s that %T does not — it can be stored but never set",
						tc.recordType, name, tc.domainType)
				}
			}
		})
	}
}

// fieldNames flattens a struct's fields, walking into embedded ones.
//
// The walk is the whole point since the domain types started embedding their
// wire objects. reflect reports an embedded struct as one field named after its
// type, so a plain loop would see domain.Task as eight fields — "Response" plus
// the seven internal ones — and report that a record covering all nineteen has
// eleven fields the type does not. Worse, it would be blind to exactly the
// twelve fields the protocol owns: an addition to uhp.Response, which is the
// change most likely to be dropped on the way to disk, would pass unnoticed.
func fieldNames(v any) map[string]bool {
	out := map[string]bool{}
	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				walk(f.Type)
				continue
			}
			out[f.Name] = true
		}
	}
	walk(reflect.TypeOf(v))
	return out
}

// The round trip has to be lossless for a task that has every field set, which
// is what makes the coverage check above worth having: matching names would
// mean nothing if a matched field were still dropped in the copy.
func TestSQLiteRecordsRoundTripInProcess(t *testing.T) {
	want := sampleTask("resp_a", "sess_a", storeEpoch)
	encoded, err := encodeTask(want)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decodeTask("resp_a", encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	assertTaskEqual(t, want, got)
}
