package nativebind

import (
	"hash/maphash"
	"reflect"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

var comparisonSeed = maphash.MakeSeed()

func nativeCompare(t reflect.Type, fields *Struct) *values.NativeCompare {
	exposed := map[string]int{}
	if fields != nil {
		for i, f := range fields.Fields {
			exposed[f.Name] = i
		}
	}
	result := &values.NativeCompare{Comparable: t.Comparable(), Zero: reflect.New(t).UnsafePointer()}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Name == "_" {
			continue
		}
		if index, ok := exposed[f.Name]; ok {
			result.Steps = append(result.Steps, values.NativeCompareStep{Field: index})
			continue
		}
		result.Steps = append(result.Steps, offsetComparison(nativeComparison(f.Type), f.Offset))
	}
	return result
}

func constantWork(_ unsafe.Pointer, limit uint64) (uint64, bool) { return 1, limit >= 1 }
func scalarComparison[T comparable]() values.NativeCompareStep {
	return values.NativeCompareStep{Field: -1,
		Equal: func(a, b unsafe.Pointer) bool { return *(*T)(a) == *(*T)(b) },
		Hash:  func(p unsafe.Pointer) uint64 { return maphash.Comparable(comparisonSeed, *(*T)(p)) },
		Work:  constantWork,
	}
}
func offsetComparison(child values.NativeCompareStep, offset uintptr) values.NativeCompareStep {
	return values.NativeCompareStep{Field: -1,
		Equal: func(a, b unsafe.Pointer) bool { return child.Equal(unsafe.Add(a, offset), unsafe.Add(b, offset)) },
		Hash:  func(p unsafe.Pointer) uint64 { return child.Hash(unsafe.Add(p, offset)) },
		Work:  func(p unsafe.Pointer, limit uint64) (uint64, bool) { return child.Work(unsafe.Add(p, offset), limit) },
	}
}
func comparisonMix(h, x uint64) uint64 { return (h ^ x) * 1099511628211 }
func nativeComparison(t reflect.Type) values.NativeCompareStep {
	switch t.Kind() {
	case reflect.Bool:
		return scalarComparison[bool]()
	case reflect.Int:
		return scalarComparison[int]()
	case reflect.Int8:
		return scalarComparison[int8]()
	case reflect.Int16:
		return scalarComparison[int16]()
	case reflect.Int32:
		return scalarComparison[int32]()
	case reflect.Int64:
		return scalarComparison[int64]()
	case reflect.Uint:
		return scalarComparison[uint]()
	case reflect.Uint8:
		return scalarComparison[uint8]()
	case reflect.Uint16:
		return scalarComparison[uint16]()
	case reflect.Uint32:
		return scalarComparison[uint32]()
	case reflect.Uint64:
		return scalarComparison[uint64]()
	case reflect.Uintptr:
		return scalarComparison[uintptr]()
	case reflect.Float32:
		return scalarComparison[float32]()
	case reflect.Float64:
		return scalarComparison[float64]()
	case reflect.Complex64:
		return scalarComparison[complex64]()
	case reflect.Complex128:
		return scalarComparison[complex128]()
	case reflect.Pointer, reflect.UnsafePointer, reflect.Chan:
		return scalarComparison[unsafe.Pointer]()
	case reflect.String:
		node := scalarComparison[string]()
		node.Work = func(p unsafe.Pointer, limit uint64) (uint64, bool) {
			n := uint64(max(1, len(*(*string)(p))))
			return n, n <= limit
		}
		return node
	case reflect.Interface:
		if t.NumMethod() == 0 {
			return scalarComparison[any]()
		}
		return values.NativeCompareStep{Field: -1,
			Equal: func(a, b unsafe.Pointer) bool {
				return reflect.NewAt(t, a).Elem().Interface() == reflect.NewAt(t, b).Elem().Interface()
			},
			Hash: func(p unsafe.Pointer) uint64 {
				return maphash.Comparable(comparisonSeed, reflect.NewAt(t, p).Elem().Interface())
			}, Work: constantWork}
	case reflect.Struct:
		var children []values.NativeCompareStep
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Name != "_" {
				children = append(children, offsetComparison(nativeComparison(f.Type), f.Offset))
			}
		}
		return values.NativeCompareStep{Field: -1,
			Equal: func(a, b unsafe.Pointer) bool {
				for _, child := range children {
					if !child.Equal(a, b) {
						return false
					}
				}
				return true
			},
			Hash: func(p unsafe.Pointer) uint64 {
				h := uint64(14695981039346656037)
				for _, child := range children {
					h = comparisonMix(h, child.Hash(p))
				}
				return h
			},
			Work: func(p unsafe.Pointer, limit uint64) (uint64, bool) {
				if limit == 0 {
					return 0, false
				}
				n := uint64(1)
				for _, child := range children {
					count, ok := child.Work(p, limit-n)
					if !ok {
						return 0, false
					}
					n += count
				}
				return n, true
			},
		}
	case reflect.Array:
		child := nativeComparison(t.Elem())
		length, stride := t.Len(), t.Elem().Size()
		return values.NativeCompareStep{Field: -1,
			Equal: func(a, b unsafe.Pointer) bool {
				for i := 0; i < length; i++ {
					offset := uintptr(i) * stride
					if !child.Equal(unsafe.Add(a, offset), unsafe.Add(b, offset)) {
						return false
					}
				}
				return true
			},
			Hash: func(p unsafe.Pointer) uint64 {
				h := uint64(14695981039346656037)
				for i := 0; i < length; i++ {
					h = comparisonMix(h, child.Hash(unsafe.Add(p, uintptr(i)*stride)))
				}
				return h
			},
			Work: func(p unsafe.Pointer, limit uint64) (uint64, bool) {
				if limit == 0 || uint64(length) > limit-1 {
					return 0, false
				}
				n := uint64(1)
				for i := 0; i < length; i++ {
					count, ok := child.Work(unsafe.Add(p, uintptr(i)*stride), limit-n)
					if !ok {
						return 0, false
					}
					n += count
				}
				return n, true
			},
		}
	default:
		return values.NativeCompareStep{Field: -1,
			Equal: func(a, b unsafe.Pointer) bool { return reflect.NewAt(t, a).Elem().Equal(reflect.NewAt(t, b).Elem()) },
			Hash: func(p unsafe.Pointer) uint64 {
				return maphash.Comparable(comparisonSeed, reflect.NewAt(t, p).Elem().Interface())
			}, Work: constantWork,
		}
	}
}
