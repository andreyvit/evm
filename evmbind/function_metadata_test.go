package evmbind_test

import (
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm/evmbind"
)

func TestReadFunctionMetadata_preserves_annotations(t *testing.T) {
	source := fstest.MapFS{"helper.go": {Data: []byte(`package helpers
// Render uses typed inputs.
//app:arg label inherited="caption"
func Render(first, second int,label string) {}
func (x *Thing) Method() {}
`)}}
	metadata, err := evmbind.ReadFunctionMetadata(source)
	if err != nil {
		t.Fatal(err)
	}
	got := metadata["Render"]
	if len(got.ParamNames) != 3 || got.ParamNames[1] != "second" || got.ParamNames[2] != "label" || got.Doc != "// Render uses typed inputs.\n//app:arg label inherited=\"caption\"\n" {
		t.Fatalf("%+v", got)
	}
	if _, ok := metadata["Method"]; ok {
		t.Fatal("included receiver method")
	}
}
