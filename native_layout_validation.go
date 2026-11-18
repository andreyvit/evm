package evm

import (
	"fmt"
	"reflect"

	"github.com/andreyvit/evm/values"
)

// Physical collection strides must agree with their element codecs before any
// address arithmetic or Go write barrier uses them.
func validateModelLayout(layout *values.NativeLayout, seen map[*values.NativeLayout]bool) error {
	if layout == nil || layout.GoType == nil || layout.New == nil || layout.Load == nil || layout.Store == nil {
		return fmt.Errorf("incomplete native model layout")
	}
	if seen[layout] {
		return nil
	}
	seen[layout] = true
	if layout.Size != uint64(layout.GoType.Size()) {
		return fmt.Errorf("native layout size mismatch for %s", layout.GoType)
	}
	if layout.Codec != nil && (layout.Codec.Encode == nil || layout.Codec.Decode == nil) {
		return fmt.Errorf("incomplete native codec for %s", layout.GoType)
	}
	var collection *values.NativeSliceLayout
	switch layout.GoType.Kind() {
	case reflect.Slice:
		collection = layout.Slice
	case reflect.Array:
		collection = layout.Array
	default:
		return nil
	}
	if collection == nil || collection.Element == nil || collection.Make == nil || collection.Copy == nil || collection.Clear == nil {
		return fmt.Errorf("incomplete native collection layout for %s", layout.GoType)
	}
	element := collection.Element
	if element.GoType != layout.GoType.Elem() || collection.ElementSize != uint64(layout.GoType.Elem().Size()) {
		return fmt.Errorf("native collection element mismatch for %s", layout.GoType)
	}
	return validateModelLayout(element, seen)
}

func validateNativeAllocation(s *values.Shape) error {
	layout := s.NativeAllocation
	if layout == nil {
		return nil
	}
	if err := validateModelLayout(layout, make(map[*values.NativeLayout]bool)); err != nil {
		return err
	}
	return validateModelShape(s, layout)
}

func validateModelShape(s *values.Shape, layout *values.NativeLayout) error {
	if s == nil || layout == nil {
		return fmt.Errorf("missing native model shape")
	}
	switch s.Kind {
	case values.TypedPairKind:
		if layout.GoType.Kind() != reflect.Pointer || layout.Codec == nil {
			return fmt.Errorf("model row requires native pointer codec")
		}
	case values.StructKind:
		if layout.GoType.Kind() != reflect.Struct || s.Native == nil || s.Native.GoType != layout.GoType || s.Cells != 1 || layout.OpaqueCell != 1 || layout.Address == nil {
			return fmt.Errorf("model row value requires matching native allocation")
		}
	case values.SliceKind:
		if layout.GoType.Kind() != reflect.Slice || layout.Slice == nil {
			return fmt.Errorf("native model slice layout mismatch")
		}
		return validateModelShape(s.Elem, layout.Slice.Element)
	case values.ArrayKind:
		if layout.GoType.Kind() != reflect.Array || layout.Array == nil || uint64(layout.GoType.Len()) != s.Len {
			return fmt.Errorf("native model array layout mismatch")
		}
		return validateModelShape(s.Elem, layout.Array.Element)
	default:
		return fmt.Errorf("unsupported native model shape kind %d", s.Kind)
	}
	return nil
}
