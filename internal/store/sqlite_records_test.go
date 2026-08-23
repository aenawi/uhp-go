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
		{name: "task", domainType: domain.Task{}, recordType: taskRecord{}, atLeast: 19},
		{name: "session", domainType: domain.Session{}, recordType: sessionRecord{}, atLeast: 8},
		{name: "artifact", domainType: domain.Artifact{}, recordType: artifactRecord{}, atLeast: 6},
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

func fieldNames(v any) map[string]bool {
	rt := reflect.TypeOf(v)
	out := make(map[string]bool, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		out[rt.Field(i).Name] = true
	}
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
