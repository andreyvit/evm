package values

import (
	"fmt"
	"hash/maphash"
	"reflect"
	"runtime"
	"unsafe"
)

// NativeValue retains an opaque Go value. Callers must account for the allocated
// interface cell. The payload is immutable; snapshots share it without copying
// the underlying Go value or changing pointer identity.
func NativeValue(x any) Value {
	if x == nil {
		return Value{}
	}
	return Value{P: unsafe.Pointer(&x)}
}

// AsNativeValue requires a NativeValueKind payload.
func (v Value) AsNativeValue() any {
	if v.P == nil {
		return nil
	}
	return *(*any)(v.P)
}

// NativeComparable reports Go type comparability. A comparable struct can still
// contain an interface holding an uncomparable value, just as in native Go.
func (v Value) NativeComparable() bool {
	x := v.AsNativeValue()
	return x == nil || reflect.TypeOf(x).Comparable()
}

// Hashes belong to this process and must not be persisted.
var nativeHashSeed = maphash.MakeSeed()

func nativeEqual(a, b Value) (equal bool, err error) {
	x, y := a.AsNativeValue(), b.AsNativeValue()
	defer recoverNativeComparison(&err, x, false)
	return x == y, nil
}

func nativeHash(v Value) (hash uint64, err error) {
	x := v.AsNativeValue()
	defer recoverNativeComparison(&err, x, true)
	return maphash.Comparable(nativeHashSeed, x), nil
}

func recoverNativeComparison(err *error, x any, hash bool) {
	if p := recover(); p != nil {
		cause, ok := p.(runtime.Error)
		if !ok {
			panic(p)
		}
		*err = &ComparisonError{Type: fmt.Sprintf("%T", x), Hash: hash, cause: cause}
	}
}
