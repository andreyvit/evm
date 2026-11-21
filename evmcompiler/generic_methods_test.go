package evmcompiler_test

import (
	"context"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

func TestImportedGenericMethods(t *testing.T) {
	pkg := evm.Package{Path: "lookup", Source: `//go:build go1.27

package lookup

type Index[K comparable] struct { Key K }
type Table[Row any] struct { Value Row }
func (t *Table[Row]) Lookup[K comparable](idx *Index[K], key K) (Row, bool) {
 return t.Value, idx.Key == key
}
`}
	for _, call := range []string{
		"table.Lookup(index, key)",
		"table.Lookup[string](index, key)",
		"(*lookup.Table[int]).Lookup[string](table, index, key)",
		"func() (int, bool) { f := table.Lookup[string]; return f(index, key) }()",
	} {
		t.Run(call, func(t *testing.T) {
			module, err := evmcompiler.Compile("guest.go", `import "lookup"
type TenantID uint64
type CustomerID uint64
func Run() int {
 table := &lookup.Table[int]{Value:42}
 index := &lookup.Index[string]{Key:"match"}
 key := "match"
 value, found := `+call+`
 if !found { return -1 }
 tenant, found := table.Lookup(&lookup.Index[TenantID]{Key:7}, TenantID(7))
 if !found { return -2 }
 customer, found := table.Lookup(&lookup.Index[CustomerID]{Key:9}, CustomerID(8))
 if found { return -3 }
 text, found := (&lookup.Table[string]{Value:"row"}).Lookup(&lookup.Index[int]{Key:3}, 3)
 if !found || text != "row" { return -4 }
 return value + tenant + customer
}`, pkg)
			if err != nil {
				t.Fatal(err)
			}
			program, err := evm.Link(module, pkg)
			if err != nil {
				t.Fatal(err)
			}
			output := make([]values.Value, 1)
			if err := evmtest.New(program).Run(context.Background(), "Run", nil, output); err != nil {
				t.Fatal(err)
			}
			if output[0].AsInt() != 126 {
				t.Fatalf("result = %d, want 126", output[0].AsInt())
			}
		})
	}
	for _, tc := range []struct{ call, want string }{
		{"table.Lookup(&lookup.Index[string]{}, 42)", "cannot use 42"},
		{"table.Lookup(&lookup.Index[string]{}, number)", "type int of number does not match inferred type string for K"},
		{"table.Lookup[int](&lookup.Index[string]{}, number)", "as *lookup.Index[int] value"},
		{"table.Lookup(&lookup.Index[TenantID]{}, CustomerID(7))", "type CustomerID of CustomerID(7) does not match inferred type TenantID for K"},
	} {
		t.Run("reject/"+tc.call, func(t *testing.T) {
			_, err := evmcompiler.Compile("wrong.go", `import "lookup"
type TenantID uint64
type CustomerID uint64
func Run() { table:= &lookup.Table[int]{}; number:=42; _=number; _,_ = `+tc.call+` }`, pkg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want key type mismatch, got %v", err)
			}
		})
	}
}
