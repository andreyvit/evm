package values

import (
	"context"
	"testing"
	"time"
)

func TestNativeTimePreservesGoValue(t *testing.T) {
	zone := time.FixedZone("shop", 4*60*60)
	for _, original := range []time.Time{
		{}, time.Date(2026, 11, 14, 18, 0, 0, 123, time.UTC),
		time.Date(2026, 11, 14, 18, 0, 0, 123, zone), time.Now(),
		time.Date(2500, 1, 1, 0, 0, 0, 123, zone), time.Time{}.In(zone),
		time.Unix(0, -1).UTC(), time.Unix(0, -1).In(zone),
	} {
		v := NativeTime(original)
		if got := v.AsTime(); got != original {
			t.Fatalf("native time changed: got %#v, want %#v", got, original)
		}
		if original != original.UTC() {
			owned, err := TimeType.Own(v, noTimeAllocation{})
			if err != nil || owned != v {
				t.Fatal("immutable native time was recopied", err)
			}
		}
		other := NativeTime(original)
		shape := &Shape{Type: TimeType, Kind: TimestampKind, Cells: 1}
		equal, err := shape.EqualContext(context.Background(), v, other, nil)
		if err != nil || !equal {
			t.Fatal("equal native times differ", err)
		}
		a, err := shape.HashContext(context.Background(), v, nil)
		if err != nil {
			t.Fatal(err)
		}
		b, err := shape.HashContext(context.Background(), other, nil)
		if err != nil || a != b {
			t.Fatal("equal native time hashes differ", err)
		}
		utc := Time(original)
		equal, err = shape.EqualContext(context.Background(), v, utc, nil)
		if err != nil || equal != (original == utc.AsTime()) {
			t.Fatal("timestamp equality differs from Go", err)
		}
	}
}

var nativeTimeSink Value

func TestNativeTimeAllocations(t *testing.T) {
	for _, tc := range []struct {
		value       time.Time
		allocations float64
	}{
		{time.Time{}, 0},
		{time.Date(2026, 11, 14, 18, 0, 0, 0, time.UTC), 0},
		{time.Unix(0, -1).UTC(), 0},
		{time.Date(2500, 1, 1, 0, 0, 0, 0, time.UTC), 1},
		{time.Date(2026, 11, 14, 18, 0, 0, 0, time.FixedZone("shop", 14400)), 1},
		{time.Now(), 1},
	} {
		if got := testing.AllocsPerRun(100, func() { nativeTimeSink = NativeTime(tc.value) }); got != tc.allocations {
			t.Fatalf("%v: got %g allocations, want %g", tc.value, got, tc.allocations)
		}
		if (NativeTimeAllocBytes(tc.value) != 0) != (tc.allocations != 0) {
			t.Fatal("allocation accounting differs", tc.value)
		}
	}
}

func BenchmarkNativeTime(b *testing.B) {
	for name, value := range map[string]time.Time{
		"utc":       time.Date(2026, 11, 14, 18, 0, 0, 123, time.UTC),
		"zone":      time.Date(2026, 11, 14, 18, 0, 0, 123, time.FixedZone("shop", 14400)),
		"monotonic": time.Now(),
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				nativeTimeSink = NativeTime(value)
			}
		})
	}
}

type noTimeAllocation struct{}

func (noTimeAllocation) ContextErr() error { return nil }
func (noTimeAllocation) ChargeAllocation(uint64) error {
	panic("native time ownership must not allocate")
}
