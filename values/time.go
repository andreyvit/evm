package values

import (
	"encoding/binary"
	"reflect"
	"time"
	"unsafe"
)

// The one nanosecond whose shifted word would collide with the zero-time
// sentinel uses a shared immutable payload: 999999999 nanoseconds, -1 second.
var timeBeforeEpoch = [...]byte{0x3b, 0x9a, 0xc9, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

// Time stores nanoseconds in one word when they fit, with zero reserved for
// time.Time{}. Dates outside that range use a 12-byte integer payload (uint32
// nanoseconds, int64 Unix seconds, big endian). No precision or date range is
// lost. Like database timestamps, the value denotes an instant, without a
// monotonic clock reading or timezone. AsTime returns UTC.
func Time(t time.Time) Value {
	if t.IsZero() {
		return Value{}
	}
	n := t.UnixNano()
	if time.Unix(0, n).Equal(t) {
		if n == -1 {
			return Bytes(timeBeforeEpoch[:])
		}
		return Uint(uint64(n) + 1)
	}
	b := make([]byte, 12)
	binary.BigEndian.PutUint32(b, uint32(t.Nanosecond()))
	binary.BigEndian.PutUint64(b[4:], uint64(t.Unix()))
	return Bytes(b)
}

// nativeTimeTag cannot be a byte-slice length. Its payload is an immutable
// copy of the full Go value, including location and monotonic clock data.
const nativeTimeTag = ^uint64(0)

// NativeTime preserves Go value semantics. Canonical UTC values keep the
// compact timestamp encoding; all other values retain a full native copy.
func NativeTime(t time.Time) Value {
	if t == t.UTC() {
		return Time(t)
	}
	p := new(time.Time)
	*p = t
	return Value{R: nativeTimeTag, P: unsafe.Pointer(p)}
}

// NativeTimeAllocBytes reports the allocation made by NativeTime.
func NativeTimeAllocBytes(t time.Time) uint64 {
	if t != t.UTC() {
		return uint64(unsafe.Sizeof(t))
	}
	if t.IsZero() {
		return 0
	}
	if n := t.UnixNano(); time.Unix(0, n).Equal(t) {
		return 0
	}
	return 12
}

func (v Value) AsTime() time.Time {
	if v.P != nil && v.R == nativeTimeTag {
		return *(*time.Time)(v.P)
	}
	if v.P == nil {
		if v.R == 0 {
			return time.Time{}
		}
		return time.Unix(0, int64(v.R-1)).UTC()
	}
	b := v.AsBytes()
	if len(b) != 12 {
		t, err := time.Parse(time.RFC3339Nano, v.AsString())
		if err != nil {
			panic(err)
		}
		return t.UTC()
	}
	if binary.BigEndian.Uint32(b) >= 1e9 {
		panic("values: invalid timestamp")
	}
	return time.Unix(int64(binary.BigEndian.Uint64(b[4:])), int64(binary.BigEndian.Uint32(b))).UTC()
}
func TimeCodec() Codec[time.Time] {
	return Codec[time.Time]{reflect.TypeFor[time.Time](), Value.AsTime, Time}
}

var TimeType = NewOwnedType("time.Time", Pair, func(v Value, b OwnershipBudget) (Value, error) {
	if v.P == nil || v.R == nativeTimeTag {
		return v, nil
	}
	if err := b.ChargeAllocation(v.R); err != nil {
		return Value{}, err
	}
	return Bytes(append([]byte(nil), v.AsBytes()...)), nil
})

// NativeTimeAddress returns the immutable native payload, or nil for compact
// instants. Callers may read or copy it, but must never mutate it.
func (v Value) NativeTimeAddress() unsafe.Pointer {
	if v.R == nativeTimeTag {
		return v.P
	}
	return nil
}
