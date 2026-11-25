package evmbind_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/modeldata"
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

// Watch stands for a method that keeps a script callback for other goroutines.
func (m Money) Watch(fn *evm.Callable) bool { return fn != nil }

type moneyHost struct{ price *Money }

func (h *moneyHost) Price(*evm.VM) *Money { return h.price }

const moneySource = `package money
 import "github.com/andreyvit/evm"
 type Money struct{ Amount int64; Code string }
 func (m Money) Valid() bool
 func (m Money) Add(o Money) Money
 func (m Money) Times(n int64) Money
 func (m *Money) Label() string
 func (m Money) Hidden() bool
 func (m Money) Watch(fn *evm.ConcurrentFunctionOf[func() int]) bool
 `

// moneyFixture is a model package with one row, Money, backed by the Go
// struct of that name, and a source schema that exposes the listed methods.
type moneyFixture struct {
	models evm.Package
	model  *kvo.Model
	amount kvo.SignedPropRef[int64]
	row    *evm.RecordType
	opt    evmbind.Options
	schema *evmbind.Schema
}

func newMoneyFixture(t *testing.T, allocate bool, methods ...string) (*moneyFixture, error) {
	t.Helper()
	schema := kvo.NewSchema()
	f := &moneyFixture{amount: kvo.NewSignedProp[int64](schema, 1, "amount", kvo.TInt64)}
	code := kvo.NewStringProp[string](schema, 2, "code", kvo.TMachineString)
	f.model = kvo.NewModel(schema, kvo.NewEntityType(schema, "Money", reflect.TypeFor[Money]()), func(b *kvo.ModelBuilder) { b.Prop(f.amount); b.Prop(code) })
	var err error
	if f.models, err = evm.ModelPackage("db", schema); err != nil {
		t.Fatal(err)
	}
	f.row = f.models.Record(f.model, false)
	if allocate {
		f.row.NativeAllocation = reflect.TypeFor[Money]()
		evm.RegisterNativeCopies(&f.models)
	}
	f.opt = evmbind.Options{Selective: true, Models: &f.models, NativePackage: reflect.TypeFor[Money]().PkgPath(), Methods: map[string][]string{"Money": methods}}
	f.schema, err = evmbind.New("money", fstest.MapFS{"money.go": {Data: []byte(moneySource)}}, f.opt)
	return f, err
}

// packages returns the environment of a host that hands out one price.
func (f *moneyFixture) packages(t *testing.T, price *Money) []evm.Package {
	t.Helper()
	f.schema.AddModelMethods(&f.models, nil)
	host, err := evmbind.New("host", fstest.MapFS{"host.go": {Data: []byte(`package host
 import "native/money"
 func Price() *money.Money
 `)}}, evmbind.Options{Imports: map[string]*evmbind.Schema{"native/money": f.schema}, Host: reflect.TypeFor[*moneyHost]()})
	if err != nil {
		t.Fatal(err)
	}
	return []evm.Package{f.models, f.schema.Bind(nil), host.Bind(&moneyHost{price: price})}
}

// A model struct keeps its Go methods: scripts call them on the model's row.
func TestNativeModelMethods(t *testing.T) {
	f, err := newMoneyFixture(t, true, "Valid", "Add", "Times", "Label")
	if err != nil {
		t.Fatal(err)
	}

	// The schema's own package cannot declare methods of another package's
	// rows: the model package gets them, once, and no other package does.
	for _, pkg := range []evm.Package{f.schema.Package(), f.schema.Bind(nil)} {
		if len(pkg.Methods) != 0 {
			t.Fatalf("schema package declares row methods: %v", pkg.Methods)
		}
	}
	earlier := f.models.Methods
	before := len(earlier)
	if n := f.schema.AddModelMethods(&f.models, nil); n != 4 {
		t.Fatalf("added %d methods", n)
	}
	if n := f.schema.AddModelMethods(&f.models, nil); n != 0 {
		t.Fatalf("added %d methods again", n)
	}
	if len(earlier) != before || len(f.models.Methods) != before+4 {
		t.Fatal("the package's earlier method list changed")
	}
	if n := f.schema.AddModelMethods(&evm.Package{Path: "other"}, nil); n != 0 {
		t.Fatalf("added %d methods to a package without the row", n)
	}
	var got []string
	for _, m := range f.models.Methods[before:] {
		switch m.Receiver {
		case f.row.Value:
			got = append(got, m.Name)
		case f.row.Pointer:
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

	price := &Money{Amount: 5, Code: "USD"}
	pkgs := f.packages(t, price)
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
	if *price != (Money{Amount: 5, Code: "USD"}) {
		t.Fatalf("native row changed: %+v", *price)
	}

	// A method needs the Go struct. An encoded row has none until the script
	// asks for a copy: nothing decodes a row behind the script's back.
	var w row2.Writer
	w.Begin(0, "", row2.Object, row2.Fast2)
	w.Put(f.amount.Code(), "", values.Int(7), row2.Signed)
	w.End()
	encoded := kvo.Encoded(row2.Trusted(w.BytesWritten())).WithModel(f.model).Value()
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
	conflict := f.models
	conflict.Methods = append(conflict.Methods[:len(conflict.Methods):len(conflict.Methods)], evm.ExternalMethod{Name: "amount", Receiver: f.row.Value, Signature: evm.Signature{Results: []*values.Type{values.Int64}}, Call: func(*evm.VM) error { return nil }})
	if _, err = evmcompiler.Compile("conflict.go", `import "db"; func Run(m *db.Money) {}`, conflict); err == nil {
		t.Fatal("a value method shadowed a property")
	}

	// Generated bindings call the same methods without reflection.
	generated, err := evmbind.Generate("money", reflect.TypeFor[Money]().PkgPath(), "bindings", fstest.MapFS{"money.go": {Data: []byte(moneySource)}}, f.opt)
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

// A callback parameter of a row method keeps its rules. The compiler looks
// them up on the row's own package, under the row's own name.
func TestNativeModelMethodCallbackRules(t *testing.T) {
	f, err := newMoneyFixture(t, true, "Watch")
	if err != nil {
		t.Fatal(err)
	}
	pkgs := f.packages(t, &Money{Amount: 5, Code: "USD"})
	if got := f.models.ConcurrentCallbackParams["Money.Watch"]; !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("callback parameters of Money.Watch: %v", got)
	}

	// A callback that only reads its captures registers.
	m, err := evmcompiler.Compile("watch.go", `import "host"
 func Run() bool { n := 3; return host.Price().Watch(func() int { return n }) }`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err = evmtest.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil || !out[0].AsBool() {
		t.Fatal(out[0].AsBool(), err)
	}

	// One that writes them does not compile.
	_, err = evmcompiler.Compile("racy.go", `import "host"
 func Run() bool { n := 3; return host.Price().Watch(func() int { n++; return n }) }`, pkgs...)
	if err == nil || !strings.Contains(err.Error(), "concurrent callback") {
		t.Fatal(err)
	}
}

// A value receiver crosses like a value argument: the wrapper reads the row's
// storage as the Go struct, so the row must allocate exactly that struct.
func TestNativeModelMethodNeedsItsAllocation(t *testing.T) {
	_, err := newMoneyFixture(t, false, "Valid")
	if err == nil || !strings.Contains(err.Error(), "requires its declared NativeAllocation type") {
		t.Fatal(err)
	}
	// A pointer receiver checks the row's backing on each call instead.
	if _, err = newMoneyFixture(t, false, "Label"); err != nil {
		t.Fatal(err)
	}
}

// Each instance of a generic model is its own row, and nothing binds the
// template's methods to them. The schema says so; it does not drop them.
func TestGenericModelMethodsAreRefused(t *testing.T) {
	models, _, err := modeldata.Declarations()
	if err != nil {
		t.Fatal(err)
	}
	const source = `package modeldata
 type Generic[T any] struct{ Name string; Hidden T }
 func (g Generic[T]) Label() string
 `
	opt := evmbind.Options{Selective: true, Only: []string{"Generic"}, Models: &models, NativePackage: reflect.TypeFor[modeldata.BoundModel]().PkgPath()}
	if _, err = evmbind.New("model", fstest.MapFS{"model.go": {Data: []byte(source)}}, opt); err != nil {
		t.Fatal(err)
	}
	opt.Methods = map[string][]string{"Generic": {"Label"}}
	_, err = evmbind.New("model", fstest.MapFS{"model.go": {Data: []byte(source)}}, opt)
	if err == nil || !strings.Contains(err.Error(), "methods of generic model Generic are not bound: Label") {
		t.Fatal(err)
	}
}
