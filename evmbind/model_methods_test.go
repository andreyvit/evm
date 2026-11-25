package evmbind_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

type Money struct {
	Amount int64  `kvo:"amount"`
	Code   string `kvo:"code"`
}

func (m Money) Valid() bool { return m.Code != "" }
func (m Money) Add(o Money) Money {
	if m.Code != o.Code {
		return Money{}
	}
	return Money{Amount: m.Amount + o.Amount, Code: m.Code}
}
func (m Money) Times(n int64) Money { return Money{Amount: m.Amount * n, Code: m.Code} }
func (m *Money) Label() string      { return m.Code }
func (m Money) Hidden() bool        { return true }

type moneyHost struct{ price *Money }

func (h *moneyHost) Price(*evm.VM) *Money { return h.price }

const moneySource = `package money
 type Money struct{ Amount int64; Code string }
 func (m Money) Valid() bool
 func (m Money) Add(o Money) Money
 func (m Money) Times(n int64) Money
 func (m *Money) Label() string
 func (m Money) Hidden() bool
 `

// A model struct keeps its Go methods: scripts call them on the model's row.
func TestNativeModelMethods(t *testing.T) {
	schema := kvo.NewSchema()
	amount := kvo.NewSignedProp[int64](schema, 1, "amount", kvo.TInt64)
	code := kvo.NewStringProp[string](schema, 2, "code", kvo.TMachineString)
	model := kvo.NewModel(schema, kvo.NewEntityType(schema, "Money", reflect.TypeFor[Money]()), func(b *kvo.ModelBuilder) { b.Prop(amount); b.Prop(code) })
	models, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	row := models.Record(model, false)
	row.NativeAllocation = reflect.TypeFor[Money]()
	evm.RegisterNativeCopies(&models)
	opt := evmbind.Options{Selective: true, Models: &models, NativePackage: reflect.TypeFor[Money]().PkgPath(), Methods: map[string][]string{"Money": {"Valid", "Add", "Times", "Label"}}}
	dep, err := evmbind.New("money", fstest.MapFS{"money.go": {Data: []byte(moneySource)}}, opt)
	if err != nil {
		t.Fatal(err)
	}

	// The schema's own package cannot declare methods of another package's rows.
	for _, pkg := range []evm.Package{dep.Package(), dep.Bind(nil)} {
		if len(pkg.Methods) != 0 {
			t.Fatalf("schema package declares row methods: %v", pkg.Methods)
		}
	}
	var got []string
	for _, m := range dep.ModelMethods(nil) {
		switch m.Receiver {
		case row.Value:
			got = append(got, m.Name)
		case row.Pointer:
			got = append(got, "*"+m.Name)
		default:
			t.Fatalf("method %s has receiver %s", m.Name, m.Receiver.Name())
		}
		if m.Call == nil {
			t.Fatalf("method %s is not bound", m.Name)
		}
	}
	if want := []string{"Valid", "Add", "Times", "*Label"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, wanted %v", got, want)
	}
	models.Methods = append(models.Methods, dep.ModelMethods(nil)...)

	host, err := evmbind.New("host", fstest.MapFS{"host.go": {Data: []byte(`package host
 import "native/money"
 func Price() *money.Money
 `)}}, evmbind.Options{Imports: map[string]*evmbind.Schema{"native/money": dep}, Host: reflect.TypeFor[*moneyHost]()})
	if err != nil {
		t.Fatal(err)
	}
	instance := &moneyHost{price: &Money{Amount: 5, Code: "USD"}}
	pkgs := []evm.Package{models, dep.Bind(nil), host.Bind(instance)}
	const script = `import("db";"host")
 type Checked interface { Valid() bool }
 func Run() int64 {
  p := host.Price()
  // A value method runs on a pointer, a variable and a call result.
  if !p.Valid() || p.Label() != "USD" { return -1 }
  sum := p.Add(*p).Times(3)
  if !sum.Valid() || sum.Label() != "USD" { return -2 }
  var zero db.Money
  if zero.Valid() || p.Add(zero).Valid() { return -3 }
  valid := sum.Valid
  times := db.Money.Times
  var checked Checked = sum
  if !valid() || times(sum, 2).amount != 60 || !checked.Valid() { return -4 }
  checked = p
  if !checked.Valid() { return -5 }
  return sum.amount
 }
 func Encoded(m *db.Money) bool { return m.Valid() }
 func Copied(m *db.Money) int64 { return m.NativeCopy().Times(2).amount }
 func Missing() bool { var m *db.Money; return m.Valid() }
 `
	m, err := evmcompiler.Compile("money.go", script, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err = evmtest.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil {
		t.Fatal(err)
	}
	if out[0].AsInt() != 30 {
		t.Fatal(out[0].AsInt())
	}
	if *instance.price != (Money{Amount: 5, Code: "USD"}) {
		t.Fatalf("native row changed: %+v", *instance.price)
	}

	// A method needs the Go struct. An encoded row has none until the script
	// asks for a copy: nothing decodes a row behind the script's back.
	var w row2.Writer
	w.Begin(0, "", row2.Object, row2.Fast2)
	w.Put(amount.Code(), "", values.Int(7), row2.Signed)
	w.End()
	encoded := kvo.Encoded(row2.Trusted(w.BytesWritten())).WithModel(model).Value()
	if err = evmtest.New(p).Run(context.Background(), "Encoded", []values.Value{encoded}, out[:]); err == nil || !strings.Contains(err.Error(), "model row requires explicit conversion") {
		t.Fatal(err)
	}
	if err = evmtest.New(p).Run(context.Background(), "Copied", []values.Value{encoded}, out[:]); err != nil || out[0].AsInt() != 14 {
		t.Fatal(out[0].AsInt(), err)
	}
	if err = evmtest.New(p).Run(context.Background(), "Missing", nil, out[:]); err == nil || !strings.Contains(err.Error(), "nil record dereference") {
		t.Fatal(err)
	}

	// Unlisted methods stay out, as for any other native type.
	if _, err = evmcompiler.Compile("hidden.go", `import "host"; func Run() bool { return host.Price().Hidden() }`, pkgs...); err == nil || !strings.Contains(err.Error(), "Hidden") {
		t.Fatal(err)
	}

	// A method cannot take the name of one of the row's properties.
	conflict := models
	conflict.Methods = append(conflict.Methods[:len(conflict.Methods):len(conflict.Methods)], evm.ExternalMethod{Name: "amount", Receiver: row.Value, Signature: evm.Signature{Results: []*values.Type{values.Int64}}, Call: func(*evm.VM) error { return nil }})
	if _, err = evmcompiler.Compile("conflict.go", `import "db"; func Run(m *db.Money) {}`, conflict); err == nil {
		t.Fatal("a value method shadowed a property")
	}

	// Generated bindings call the same methods without reflection.
	generated, err := evmbind.Generate("money", reflect.TypeFor[Money]().PkgPath(), "bindings", fstest.MapFS{"money.go": {Data: []byte(moneySource)}}, opt)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"Money.Valid": func`, `readTypeMoney(f, f.CallArgument(0)).Add(readTypeMoney(f, f.CallArgument(1)))`, `"*Money.Label": func`} {
		if !strings.Contains(string(generated), want) {
			t.Fatalf("generated bindings lack %s:\n%s", want, generated)
		}
	}
	if strings.Contains(string(generated), "Hidden") {
		t.Fatal("generated bindings include an unlisted method")
	}
}
