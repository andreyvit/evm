package evmbind_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/msgpack"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

// Coin is a small domain value: a host struct, a stored sub-object, and a
// script type with methods, all at once.
type Coin struct {
	Amount int64  `kvo:"amount"`
	Code   uint32 `kvo:"code"`
}

// coinSource is what scripts compile. Hosts keep the same code as Go.
const coinSource = `package money

type Coin struct {
	Amount int64  ` + "`kvo:\"amount\"`" + `
	Code   uint32 ` + "`kvo:\"code\"`" + `
}

func Make(amount int64, code uint32) Coin { return Coin{amount, code} }

// The host runs Parse; the source only names it.
func Parse(s string) (Coin, error)

func (c Coin) Valid() bool { return c.Code != 0 }

func (c Coin) Add(o Coin) Coin {
	if c.Code != o.Code {
		return Coin{}
	}
	return Coin{Amount: c.Amount + o.Amount, Code: c.Code}
}
`

var coinCodes [1 << 10]byte

// coinCell keeps a Coin in one cell: the amount in R, and in P a pointer
// into a static table that stands for the code. Equal coins have equal cells.
func coinCell() *values.CellStruct {
	code := func(v values.Value) uint32 {
		if v.P == nil {
			return 0
		}
		return uint32(uintptr(v.P) - uintptr(unsafe.Pointer(&coinCodes[0])))
	}
	pack := func(c Coin) values.Value {
		v := values.Value{R: uint64(c.Amount)}
		if c.Code != 0 {
			v.P = unsafe.Pointer(&coinCodes[c.Code])
		}
		return v
	}
	return &values.CellStruct{
		// The amount takes all of R, so the cell carries no type tag.
		Untagged: true,
		Type:     values.NewType("money.Coin", values.Pair),
		Get: func(v values.Value, i int) values.Value {
			if i == 0 {
				return values.Int(int64(v.R))
			}
			return values.Uint(uint64(code(v)))
		},
		Pack:        func(f [2]values.Value) values.Value { return pack(Coin{f[0].AsInt(), uint32(f[1].R)}) },
		LoadNative:  func(p unsafe.Pointer) values.Value { return pack(*(*Coin)(p)) },
		StoreNative: func(p unsafe.Pointer, v values.Value) { *(*Coin)(p) = Coin{int64(v.R), code(v)} },
	}
}

func parseCoin(s string) (Coin, error) {
	if s == "" {
		return Coin{}, errors.New("no coin")
	}
	return Coin{Amount: int64(len(s)), Code: 7}, nil
}

type coinLine struct {
	Title string `kvo:"title"`
	Price Coin   `kvo:"price"`
	Paid  *Coin  `kvo:"paid"`
}

type coinHost struct{}

func (coinHost) Double(_ *evm.VM, c Coin) Coin { return Coin{c.Amount * 2, c.Code} }

// A struct declared in package source can share its identity and storage with
// a host struct: its methods run as script code, host functions take and
// return it, and it lives in one cell.
//
// A value model then makes a stored sub-object read as that struct, on any
// row, without a Go struct behind it.
func TestValueModel(t *testing.T) {
	schema := kvo.NewSchema()
	amount := kvo.NewSignedProp[int64](schema, 1, "amount", kvo.TInt64)
	code := kvo.NewUnsignedProp[uint32](schema, 2, "code", kvo.TUint32)
	title := kvo.NewStringProp[string](schema, 3, "title", kvo.TMachineString)
	coinType := kvo.NewEntityType(schema, "Coin", reflect.TypeFor[Coin]())
	price := kvo.NewRecordProp(schema, 4, "price", coinType)
	paid := kvo.NewRecordProp(schema, 5, "paid", coinType)
	coinModel := kvo.NewModel(schema, coinType, func(b *kvo.ModelBuilder) { b.Prop(amount); b.Prop(code) })
	lineModel := kvo.NewModel(schema, kvo.NewEntityType(schema, "Line", reflect.TypeFor[coinLine]()), func(b *kvo.ModelBuilder) { b.Prop(title); b.Prop(price); b.Prop(paid) })
	withRows, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	// Scripts never see the sub-object as a row, so its row types can go.
	models := evm.WithoutRecords(withRows, coinModel)

	cell := coinCell()
	native, err := evmbind.New("money", fstest.MapFS{"money.go": {Data: []byte(coinSource)}}, evmbind.Options{
		Only: []string{"Coin"}, NativePackage: reflect.TypeFor[Coin]().PkgPath(), Types: []reflect.Type{reflect.TypeFor[Coin]()},
		CellStructs: map[string]*values.CellStruct{"Coin": cell},
		Functions:   map[string]any{"Parse": parseCoin},
	})
	if err != nil {
		t.Fatal(err)
	}
	bound := native.Bind(nil)
	money := evm.Package{Path: "money", Source: coinSource, Functions: bound.Functions, ValueModels: []evm.ValueModel{{Model: coinModel, Type: cell.Type, Props: [2]uint64{amount.Code(), code.Code()}}}}
	for _, d := range bound.Types {
		if d.Name == "Coin" {
			d.Name, d.SourceName = "", "Coin" // the source declares it
		}
		money.Types = append(money.Types, d)
	}
	host, err := evmbind.New("host", fstest.MapFS{"host.go": {Data: []byte("package host\nimport \"native/money\"\nfunc Double(c money.Coin) money.Coin\n")}},
		evmbind.Options{Imports: map[string]*evmbind.Schema{"native/money": native}, Host: reflect.TypeFor[coinHost]()})
	if err != nil {
		t.Fatal(err)
	}
	pkgs := []evm.Package{models, money, host.Bind(coinHost{})}

	m, err := evmcompiler.Compile("coins.go", `import("db";"host";"money")
type Checked interface { Valid() bool }
// Values runs without any row: literals, methods from source, host calls.
func Values() int64 {
	a := money.Coin{Amount: 2, Code: 7}
	sum := host.Double(a).Add(money.Make(3, 7))
	var zero money.Coin
	if !sum.Valid() || zero.Valid() || a.Add(money.Make(1, 8)).Valid() { return -1 }
	if sum != money.Make(7, 7) || sum == a { return -2 }
	var c Checked = sum
	seen := map[money.Coin]int{sum: 1}
	ptr := &sum
	if !c.Valid() || seen[money.Make(7, 7)] != 1 || !ptr.Valid() || ptr.Amount != 7 { return -3 }
	parsed, err := money.Parse("four")
	_, bad := money.Parse("")
	if err != nil || bad == nil || parsed != money.Make(4, 7) { return -4 }
	return sum.Amount*10 + int64(sum.Code)
}
// Paid reads two stored sub-objects of a row as values.
func Paid(line *db.Line) int64 {
	if !line.price.Valid() { return -1 }
	if !line.paid.Valid() { return line.price.Amount }
	return line.price.Add(line.paid).Amount*10 + int64(line.paid.Code)
}
func Valid(line *db.Line) bool { return line.price.Valid() }
func None() bool { var line db.Line; return line.price.Valid() || line.price != (money.Coin{}) }
`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	var out [1]values.Value
	if err = evmtest.New(p).Run(context.Background(), "Values", nil, out[:]); err != nil || out[0].AsInt() != 77 {
		t.Fatal(out[0].AsInt(), err)
	}
	if err = evmtest.New(p).Run(context.Background(), "None", nil, out[:]); err != nil || out[0].AsBool() {
		t.Fatal(out[0].AsBool(), err)
	}

	// The same script reads every storage form of the row.
	binding := kvo.Bind(reflect.TypeFor[coinLine](), lineModel)
	for _, format := range []string{"native", "MessagePack", "Fast2", "Compact2", "memory"} {
		record := func(line *coinLine) values.Value {
			switch format {
			case "native":
				return binding.Record(reflect.ValueOf(line)).Value()
			case "MessagePack":
				return binding.MessagePack(msgpack.Encode(nil, reflect.ValueOf(line))).Value()
			}
			layout := row2.Fast2
			if format == "Compact2" {
				layout = row2.Compact2
			}
			r := kvo.Encoded(row2.Open(binding.Encode(nil, reflect.ValueOf(line).Elem(), layout))).WithModel(lineModel)
			if format == "memory" {
				r = r.Fastest()
			}
			return r.Value()
		}
		for _, tc := range []struct {
			line coinLine
			want int64
		}{
			{coinLine{Title: "no price"}, -1},
			{coinLine{Price: Coin{5, 7}}, 5},
			{coinLine{Price: Coin{5, 7}, Paid: &Coin{4, 7}}, 97},
			{coinLine{Price: Coin{5, 7}, Paid: &Coin{4, 8}}, 8}, // codes differ: the sum is the zero coin
		} {
			if err = evmtest.New(p).Run(context.Background(), "Paid", []values.Value{record(&tc.line)}, out[:]); err != nil || out[0].AsInt() != tc.want {
				t.Fatalf("%s %+v: got %d, wanted %d (%v)", format, tc.line, out[0].AsInt(), tc.want, err)
			}
		}
	}

	// Reading a stored value and calling its method allocates nothing, for an
	// encoded row and for a Go-backed one.
	valid, err := p.LookupFunction("Valid", nil)
	if err != nil {
		t.Fatal(err)
	}
	line := &coinLine{Price: Coin{5, 7}}
	for name, row := range map[string]values.Value{
		"native":  binding.Record(reflect.ValueOf(line)).Value(),
		"encoded": kvo.Encoded(row2.Open(binding.Encode(nil, reflect.ValueOf(line).Elem(), row2.Fast2))).WithModel(lineModel).Value(),
	} {
		vm := p.AcquireVM(t.Context())
		slots := [2]values.Value{row}
		allocs := testing.AllocsPerRun(100, func() {
			slots[0] = row
			if err := vm.Run(valid, slots[:]); err != nil || slots[1].R != 1 {
				t.Fatal(slots, err)
			}
		})
		vm.Release()
		if allocs != 0 {
			t.Fatalf("%s row: %v allocations per read", name, allocs)
		}
	}

	// The value is a copy. A script can change its own copy, but it cannot
	// write a stored field through the row or take the field's address.
	for source, ok := range map[string]bool{
		`func Run(line *db.Line) int64 { c := line.price; c.Amount = 9; return c.Amount + line.price.Amount }`: true,
		`func Run(line *db.Line) { line.price.Amount = 9 }`:                                                    false,
		`func Run(line *db.Line) { line.price = money.Make(1, 2) }`:                                            false,
		`func Run(line *db.Line) *money.Coin { return &line.price }`:                                           false,
	} {
		_, err := evmcompiler.Compile("copy.go", "import(\"db\";\"money\")\nvar _ = money.Make\n"+source, pkgs...)
		if (err == nil) != ok {
			t.Fatalf("%s: %v", source, err)
		}
	}

	// An editable row does not expose the property yet.
	if _, err = evmcompiler.Compile("edit.go", `import "db"; func Run(line *db.LineEdit) bool { return line.price.Valid() }`, pkgs...); err == nil || !strings.Contains(err.Error(), "price") {
		t.Fatal(err)
	}
	// A cell is not a record handle: a program that reads a sub-object as a
	// struct links only against the value model it was compiled with.
	undeclared := money
	undeclared.ValueModels = nil
	if _, err = evm.Link(m, models, undeclared, pkgs[2]); err == nil || !strings.Contains(err.Error(), "does not read as money.Coin") {
		t.Fatal(err)
	}
	// And the other way round: a program compiled when the sub-object was
	// still a row does not link once it reads as a struct value.
	for _, source := range []string{
		`func Run(line *db.Line) bool { return line.price != nil }`,
		`func Run(line *db.Line) int64 { return line.price.amount }`,
		`func Run(line *db.LineEdit) bool { return line.price != nil }`,
	} {
		old, err := evmcompiler.Compile("old.go", "import \"db\"\n"+source, withRows)
		if err != nil {
			t.Fatal(source, err)
		}
		if _, err = evm.Link(old, pkgs...); err == nil || !strings.Contains(err.Error(), "reads only as a money.Coin value") {
			t.Fatal(source, err)
		}
	}

	// Source declares the struct as the host has it. A field reads a cell by
	// position, so swapped fields would read each other's value.
	swapped := money
	swapped.Source = strings.NewReplacer(
		"Amount int64  `kvo:\"amount\"`\n\tCode   uint32 `kvo:\"code\"`", "Code   uint32 `kvo:\"code\"`\n\tAmount int64  `kvo:\"amount\"`",
		"Coin{amount, code}", "Coin{Amount: amount, Code: code}",
	).Replace(coinSource)
	if swapped.Source == coinSource {
		t.Fatal("fields not swapped")
	}
	if _, err = evmcompiler.Compile("swapped.go", `import "money"; func Run(c money.Coin) int64 { return c.Amount }`, models, swapped, pkgs[2]); err == nil || !strings.Contains(err.Error(), "struct money.Coin does not match its host declaration") {
		t.Fatal(err)
	}

	// The type prints as Go prints it: by its package name, not its path.
	named, err := evmcompiler.Compile("name.go", `import("fmt";"money")
func Run() string { c := money.Make(4, 7); return fmt.Sprintf("%T %v", c, c) }`, pkgs...)
	if err != nil {
		t.Fatal(err)
	}
	if p, err = evm.Link(named, pkgs...); err != nil {
		t.Fatal(err)
	}
	if err = evmtest.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].AsString() != "money.Coin {4 7}" {
		t.Fatal(out[0].AsString(), err)
	}

	// A concurrent callback must not write what it shares. A captured value
	// is shared, but a copy of it is the callback's own: two scalars alias
	// nothing.
	hooks, err := evmbind.New("hooks", fstest.MapFS{"api.go": {Data: []byte("package hooks\nimport \"github.com/andreyvit/evm\"\nfunc Add(hook *evm.ConcurrentFunctionOf[func(int64) int64])\n")}},
		evmbind.Options{Selective: true, Functions: map[string]any{"Add": func(*evm.Callable) {}}})
	if err != nil {
		t.Fatal(err)
	}
	for body, ok := range map[string]bool{
		`c := v; c.Amount += n; return c.Amount`: true,
		`return v.Add(v).Amount + n`:             true,
		`v.Amount = n; return n`:                 false,
	} {
		_, err := evmcompiler.Compile("hook.go", "import(\"hooks\";\"money\")\nfunc Register() { v := money.Make(1, 7); hooks.Add(func(n int64) int64 { "+body+" }) }", append(pkgs[:len(pkgs):len(pkgs)], hooks.Bind(nil))...)
		if (err == nil) != ok || err != nil && !strings.Contains(err.Error(), "concurrent callback") {
			t.Fatalf("%s: %v", body, err)
		}
	}
}

// A collection decodes its elements as rows, so a model that reads as a
// struct value cannot be a list element yet. The compiler says so.
func TestValueModelCollectionsAreRefused(t *testing.T) {
	schema := kvo.NewSchema()
	amount := kvo.NewSignedProp[int64](schema, 1, "amount", kvo.TInt64)
	code := kvo.NewUnsignedProp[uint32](schema, 2, "code", kvo.TUint32)
	coinType := kvo.NewEntityType(schema, "Coin", reflect.TypeFor[Coin]())
	coins := kvo.NewRecordListProp(schema, 3, "coins", kvo.ListOf(coinType))
	coinModel := kvo.NewModel(schema, coinType, func(b *kvo.ModelBuilder) { b.Prop(amount); b.Prop(code) })
	kvo.NewModel(schema, kvo.NewEntityType(schema, "Purse"), func(b *kvo.ModelBuilder) { b.Prop(coins) })
	models, err := evm.ModelPackage("db", schema)
	if err != nil {
		t.Fatal(err)
	}
	cell := coinCell()
	native, err := evmbind.New("money", fstest.MapFS{"money.go": {Data: []byte(coinSource)}}, evmbind.Options{
		Only: []string{"Coin"}, NativePackage: reflect.TypeFor[Coin]().PkgPath(), Types: []reflect.Type{reflect.TypeFor[Coin]()},
		CellStructs: map[string]*values.CellStruct{"Coin": cell},
	})
	if err != nil {
		t.Fatal(err)
	}
	money := evm.Package{Path: "money", Source: coinSource, ValueModels: []evm.ValueModel{{Model: coinModel, Type: cell.Type, Props: [2]uint64{amount.Code(), code.Code()}}}}
	for _, d := range native.Package().Types {
		if d.Name == "Coin" {
			d.Name, d.SourceName = "", "Coin"
		}
		money.Types = append(money.Types, d)
	}
	_, err = evmcompiler.Compile("purse.go", `import "db"; func Run(p *db.Purse) int { return len(p.coins) }`, evm.WithoutRecords(models, coinModel), money)
	if err == nil || !strings.Contains(err.Error(), "a collection of Coin is not supported") {
		t.Fatal(err)
	}

	// A declaration that does not fit the struct is refused when it is read.
	bad := money
	bad.ValueModels = []evm.ValueModel{{Model: coinModel, Type: cell.Type, Props: [2]uint64{code.Code(), amount.Code()}}}
	if _, err = evmcompiler.Compile("bad.go", `func Run() {}`, models, bad); err == nil || !strings.Contains(err.Error(), "does not match a one-word property") {
		t.Fatal(err)
	}
}
