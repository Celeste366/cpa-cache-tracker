package storage

import (
	"cpa-cache-tracker/abi"
	"cpa-cache-tracker/fingerprint"
	"cpa-cache-tracker/tracker"
	"fmt"
	"reflect"
	"testing"
)

func TestSessionIndexDoesNotRetainEvictedFingerprints(t *testing.T) {
	s, err := Open(t.TempDir(), abi.DefaultPluginConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var first tracker.Record
	for i := 0; i < 510; i++ {
		r, err := s.Append(tracker.Record{Model: "m", SessionID: fmt.Sprint(i), HasFingerprint: true, TraceEnabled: true, Fingerprint: &fingerprint.Result{SystemHash: "s", MessageHashes: []string{"m"}}, Tokens: tracker.NormalizedTokens{EffectiveInput: 100}})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = r
		}
	}
	// The session index may hold compact values/IDs, but never full old fingerprints.
	for _, key := range reflect.ValueOf(s.lastBySessionModel).MapKeys() {
		v := reflect.ValueOf(s.lastBySessionModel).MapIndex(key)
		if v.Kind() == reflect.Pointer {
			v = v.Elem()
		}
		if v.Kind() == reflect.Struct {
			f := v.FieldByName("Fingerprint")
			if f.IsValid() && !f.IsNil() {
				t.Fatal("session index retains fingerprints outside bounded LRU")
			}
		}
	}
	next, err := s.Append(tracker.Record{Model: "m", SessionID: "0", Tokens: tracker.NormalizedTokens{EffectiveInput: 100}})
	if err != nil || next.PrevID != first.ID {
		t.Fatalf("evicted predecessor cannot be resolved from disk: %v %+v", err, next)
	}
}
