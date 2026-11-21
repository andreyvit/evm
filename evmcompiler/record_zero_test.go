package evmcompiler_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

func TestRecordZeroValue(t *testing.T) {
	schema := kvo.NewSchema()
	entity := kvo.NewEntityType(schema, "Row")
	kvo.NewProp(schema, 1, "name", kvo.TMachineString, nil)
	kvo.NewProp(schema, 2, "count", kvo.TInt64, nil)
	kvo.NewProp(schema, 3, "tags", kvo.ListOf(kvo.TMachineString), nil)
	kvo.NewProp(schema, 4, "child", entity, nil)
	kvo.NewProp(schema, 5, "kids", kvo.ListOf(entity), nil)
	kvo.NewModel(schema, entity, func(b *kvo.ModelBuilder) {
		for code := uint64(1); code <= 5; code++ {
			b.Prop(schema.MustPropByCode(code))
		}
	})
	db, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	mod, err := evmcompiler.Compile("zero.go", `import "db"
func Read()bool{var r db.Row;return r.name=="" && r.count==0 && r.child==nil && len(r.tags)==0}
func Copy()bool{var r db.Row;p:=&r;v:=*p;return v.name=="" && v.count==0}
func Alloc()bool{p:=new(db.Row);return p.name=="" && p.count==0}
func Aggregate()bool{var a [2]db.Row;var s struct{Row db.Row};return a[1].name=="" && s.Row.count==0}
func ObjectList()bool{var r db.Row;return r.kids==nil && len(r.kids)==0}
func LocalIndex()string{var r db.Row;tags:=r.tags;return tags[0]}
func ObjectIndex()*db.Row{var r db.Row;return r.kids[0]}
func EditRead()string{var r db.RowEdit;return r.name}
func EditCopy()string{var r db.RowEdit;p:=&r;v:=*p;return v.name}
func List()bool{var r db.Row;tags:=r.tags;for range tags{return false};return len(tags)==0}
func Index()string{var r db.Row;return r.tags[0]}
func Child()int64{var r db.Row;return r.child.count}
func NilRead()string{var p *db.Row;return p.name}
func NilCopy()string{var p *db.Row;r:=*p;return r.name}
`, db)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(mod, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Read", "Copy", "List", "Alloc", "Aggregate", "ObjectList"} {
		out := make([]values.Value, 1)
		if err := evmtest.New(program).Run(context.Background(), name, nil, out); err != nil {
			t.Errorf("%s: %v", name, err)
		} else if !out[0].AsBool() {
			t.Errorf("%s: nonzero field", name)
		}
	}
	for _, tc := range []struct{ name, message string }{
		{"NilRead", "missing Row record"},
		{"NilCopy", "nil record dereference"},
		{"Child", "missing Row record"},
		{"EditRead", "invalid RowEdit record"},
		{"EditCopy", "invalid RowEdit record"},
	} {
		err := evmtest.New(program).Run(context.Background(), tc.name, nil, make([]values.Value, 1))
		if err == nil || !strings.Contains(err.Error(), tc.message) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	for _, name := range []string{"Index", "LocalIndex", "ObjectIndex"} {
		if err := evmtest.New(program).Run(context.Background(), name, nil, make([]values.Value, 1)); !errors.Is(err, evm.ErrBounds) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
