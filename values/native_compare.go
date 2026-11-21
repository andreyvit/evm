package values

import (
	"context"
	"runtime"
	"unsafe"
)

func (s *Shape) nativeComparison() *NativeCompare {
	if s.Native != nil && s.Native.OpaqueCell != 0 {
		return s.Native.Compare
	}
	return nil
}
func (s *Shape) nativeSnapshot(v Value) unsafe.Pointer {
	if v.P != nil {
		if p := (*Value)(CellAt(v.P, s.Native.OpaqueCell-1)).P; p != nil {
			return p
		}
	}
	return s.Native.Compare.Zero
}
func (s *Shape) equalNative(ctx context.Context, a, b Value, ts *TypeTable, depth int) (equal bool, err error) {
	if !s.Native.Compare.Comparable {
		return false, &ComparisonError{Type: s.Type.Name()}
	}
	x, y := s.nativeSnapshot(a), s.nativeSnapshot(b)
	for i, step := range s.Native.Compare.Steps {
		if i&255 == 255 {
			if err := ctx.Err(); err != nil {
				return false, err
			}
		}
		if step.Field >= 0 {
			f := &s.Fields[step.Field]
			equal, err := f.Shape.equal(ctx, f.Shape.Stored(CellAt(a.P, f.Offset)), f.Shape.Stored(CellAt(b.P, f.Offset)), ts, depth)
			if err != nil || !equal {
				return equal, err
			}
		} else {
			equal, err := compareNativeStep(step, x, y, s.Type.Name())
			if err != nil || !equal {
				return equal, err
			}
		}
	}
	return true, nil
}
func (s *Shape) hashNative(ctx context.Context, v Value, ts *TypeTable, depth int) (hash uint64, err error) {
	if !s.Native.Compare.Comparable {
		return 0, &ComparisonError{Type: s.Type.Name(), Hash: true}
	}
	p := s.nativeSnapshot(v)
	h := uint64(14695981039346656037)
	for i, step := range s.Native.Compare.Steps {
		if i&255 == 255 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		var n uint64
		if step.Field >= 0 {
			f := &s.Fields[step.Field]
			n, err = f.Shape.hash(ctx, f.Shape.Stored(CellAt(v.P, f.Offset)), ts, depth)
			if err != nil {
				return 0, err
			}
		} else {
			n, err = hashNativeStep(step, p, s.Type.Name())
			if err != nil {
				return 0, err
			}
		}
		h = hashMix(h, n)
	}
	return h, nil
}
func (s *Shape) workNative(v Value, limit uint64, ts *TypeTable) (uint64, bool) {
	if limit == 0 {
		return 0, false
	}
	p := s.nativeSnapshot(v)
	n := uint64(1)
	for _, step := range s.Native.Compare.Steps {
		var count uint64
		var ok bool
		if step.Field >= 0 {
			f := &s.Fields[step.Field]
			count, ok = f.Shape.Work(f.Shape.Stored(CellAt(v.P, f.Offset)), limit-n, ts)
		} else {
			count, ok = step.Work(p, limit-n)
		}
		if !ok {
			return 0, false
		}
		n += count
	}
	return n, true
}
func recoverNativeTypeComparison(err *error, name string, hash bool) {
	if p := recover(); p != nil {
		if cause, ok := p.(runtime.Error); ok {
			*err = &ComparisonError{Type: name, Hash: hash, cause: cause}
		} else {
			panic(p)
		}
	}
}

func compareNativeStep(step NativeCompareStep, a, b unsafe.Pointer, name string) (equal bool, err error) {
	defer recoverNativeTypeComparison(&err, name, false)
	return step.Equal(a, b), nil
}
func hashNativeStep(step NativeCompareStep, p unsafe.Pointer, name string) (hash uint64, err error) {
	defer recoverNativeTypeComparison(&err, name, true)
	return step.Hash(p), nil
}
