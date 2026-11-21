package evmcompiler

import (
	"go/types"
	"reflect"
	"testing"

	"github.com/andreyvit/evm/values"
)

// An overlay must own every slice and map a compile writes. A field that an
// overlay shares with its base would let concurrent compiles write the same
// memory, even through an empty slice that still has capacity.
func TestOverlayOwnsWhatCompilesWrite(t *testing.T) {
	env, err := NewEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	base := *env.real
	base.shaping = map[*values.Shape]bool{}
	base.syntheticTypes = map[*values.Shape]reflect.Type{}
	base.pendingNativeStorage = make([]nativeStorageShape, 0, 4)
	base.pendingSourceChecks = make([]sourceInstanceCheck, 0, 4)
	base.pendingMapKeys = make([]types.Type, 0, 4)
	base.using = make([]string, 0, 4)
	o := base.overlay(env.NewFileSet())
	if o.base != &base || !o.frozen {
		t.Fatal("the overlay does not read a frozen base")
	}
	// Written while compiling: must be empty and must not alias the base.
	owned := map[string]bool{"toGo": true, "fromGo": true, "shapes": true, "shapeOrder": true, "nativeLayouts": true, "syntheticTypes": true, "nativeSourceMemo": true, "pendingSourceChecks": true, "pendingMapKeys": true, "pendingNativeStorage": true, "shaping": true, "buildingShapes": true, "resolving": true, "using": true}
	// Set per type check.
	perCheck := map[string]bool{"base": true, "files": true, "guest": true}
	a, b := reflect.ValueOf(&base).Elem(), reflect.ValueOf(o).Elem()
	for i := 0; i < a.NumField(); i++ {
		name := a.Type().Field(i).Name
		x, y := a.Field(i), b.Field(i)
		switch {
		case perCheck[name]:
		case owned[name]:
			switch y.Kind() {
			case reflect.Map, reflect.Slice:
				if y.Len() != 0 {
					t.Errorf("%s: the overlay starts with %d entries", name, y.Len())
				}
				if !y.IsNil() && !x.IsNil() && y.Pointer() == x.Pointer() {
					t.Errorf("%s: the overlay shares the base's memory", name)
				}
				if y.Kind() == reflect.Slice && y.Cap() != 0 {
					t.Errorf("%s: the overlay can append into the base's array", name)
				}
			case reflect.Struct: // typeutil.Map
				if !y.IsZero() {
					t.Errorf("%s: the overlay starts with the base's entries", name)
				}
			default:
				t.Errorf("%s: unexpected kind %s", name, y.Kind())
			}
		default:
			// Everything else is shared and must therefore never be written
			// after freezing. A new field needs a decision; list it above if
			// compiles write it.
			switch y.Kind() {
			case reflect.Map, reflect.Slice:
				if x.IsNil() != y.IsNil() || !y.IsNil() && y.Pointer() != x.Pointer() {
					t.Errorf("%s: the overlay does not share the base's value", name)
				}
			case reflect.Bool, reflect.Int:
			default:
				t.Errorf("%s: unexpected kind %s", name, y.Kind())
			}
		}
	}
	for name := range owned {
		if _, ok := a.Type().FieldByName(name); !ok {
			t.Errorf("%s is not a field", name)
		}
	}
}
