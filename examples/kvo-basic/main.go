// Command kvo-basic reads and edits the same records from Go and from a
// script, whether a record is a Go struct or an encoded row.
//
// model.go declares the customer model. _script/rules.go is the script: it
// sees the model as package "m". This file makes one customer in each
// representation, uses them from Go, then compiles the script and runs it
// on the same records.
//
//	go run ./examples/kvo-basic
package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"reflect"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmstd"
	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/msgpack"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

// The underscore keeps the go tool from building the script as host code.
//
//go:embed _script/rules.go
var rulesSource string

func main() {
	run(os.Stdout)
}

func run(out io.Writer) {
	// One customer per representation. A database would hand out the two
	// encoded rows; here the binding encodes them from structs.
	ada := &Customer{Name: "Ada", Tags: []string{"vip"}, Earned: 1250, Redeemed: 250, Joined: date(2024, 1, 15)}
	bob := &Customer{Name: "Bob", Tags: []string{"newsletter"}, Earned: 300, Redeemed: 100, Joined: date(2025, 2, 1)}
	cyd := &Customer{Name: "Cyd", Earned: 80, Joined: date(2025, 2, 20)}
	bobRow := Customers.Encode(nil, reflect.ValueOf(bob).Elem(), row2.Fast2)
	cydRow := msgpack.Encode(nil, reflect.ValueOf(cyd))

	// A record is a 16-byte handle that views the struct or the row's bytes
	// in place. Opening a row decodes nothing.
	records := []kvo.Record{
		Customers.Record(reflect.ValueOf(ada)),
		Customers.Encoded(row2.Open(bobRow)),
		Customers.MessagePack(cydRow),
	}

	// Go reads typed properties the same way from every representation.
	for _, c := range records {
		name := c.Get(Customer_Name)     // string
		tags := c.Get(Customer_Tags)     // kvo.List[string]
		joined := c.Get(Customer_Joined) // time.Time
		fmt.Fprintf(out, "%s: %d points, %d tags, joined %s\n", name, PointBalance(c), tags.Len(), joined.Format(time.DateOnly))
	}

	// Go edits through an editor. Editing a struct writes to the struct.
	// Editing a row builds a new row and leaves the old bytes alone.
	for i, c := range records {
		edit := c.Edit()
		edit.Set(Customer_Earned, edit.Get(Customer_Earned)+100)
		edit.List(Customer_Tags).AddUnique("welcomed")
		records[i] = edit.Record()
	}
	fmt.Fprintf(out, "Ada's struct: %d earned, tags %v\n", ada.Earned, ada.Tags)
	fmt.Fprintf(out, "Bob's old row: %d earned; new row: %d earned, tags %v\n",
		Customers.Encoded(row2.Open(bobRow)).Get(Customer_Earned),
		records[1].Get(Customer_Earned), records[1].Get(Customer_Tags).Slice())

	// Scripts see the schema as package "m": type m.Customer with a field
	// per property. PointBalance becomes a method of it.
	models := must(evm.ModelPackage("m", Schema))
	ensure(evmbind.RegisterMethods(&models, []evmbind.Method{
		evmbind.RecordMethod(CustomerModel, "PointBalance", PointBalance, evmbind.BorrowOnly),
	}, evmbind.MethodOptions{}))
	packages := append(evmstd.Packages(), models)

	// Compile and link once.
	module := must(evmcompiler.Compile("rules.go", rulesSource, packages...))
	limits := evm.Limits{Steps: 100_000, Duration: time.Second}
	program := must(evm.LinkOptions{DefaultLimits: &limits}.Link(module, packages...))
	describe := must(program.LookupFunction("Describe", &evm.Signature{Params: []*values.Type{models.Record(CustomerModel, false).Pointer, values.TimeType}, Results: []*values.Type{values.StringType}}))
	redeem := must(program.LookupFunction("Redeem", &evm.Signature{Params: []*values.Type{models.Record(CustomerModel, true).Pointer, values.Int64}, Results: []*values.Type{values.BoolType}}))
	ctx, scope := evm.WithRecords(context.Background())
	defer scope.Close()
	vm := program.AcquireVM(ctx)
	defer vm.Release()
	// The script reads the same records. Nothing is converted or copied.
	now := date(2025, 3, 1)
	for _, c := range records {
		slots := [3]values.Value{c.Value(), values.NativeTime(now)}
		ensure(vm.Run(describe, slots[:]))
		fmt.Fprintln(out, slots[2].AsString())
	}

	// The script edits through the host's editors. They live in a record
	// scope, which a host opens for the length of a transaction.

	for i, c := range records {
		edit := c.Edit()
		slots := [3]values.Value{scope.EditRecord(CustomerModel, edit), values.Int(250)}
		ensure(vm.Run(redeem, slots[:]))
		records[i] = edit.Record()
		fmt.Fprintf(out, "%s redeems 250: %v, balance %d\n", c.Get(Customer_Name), slots[2].AsBool(), PointBalance(records[i]))
	}
	fmt.Fprintf(out, "Ada's struct: %d redeemed\n", ada.Redeemed)
}

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func must[T any](value T, err error) T {
	ensure(err)
	return value
}

func ensure(err error) {
	if err != nil {
		panic(err)
	}
}
