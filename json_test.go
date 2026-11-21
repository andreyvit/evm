package evm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

func TestScriptJSON(t *testing.T) {
	mod, err := evmcompiler.Compile("example.go", `func Result() any {
		return map[string]any{"rows": []any{false, int8(-4), nil, uint64(1<<63)+1, "\n"}, "empty": []int{}, "array":[2]float32{1.25,2}}
	}
	func Cycle() any { m:=map[string]any{}; m["m"]=m; return m }
	func Bad() any { return map[int]int{} }
	func Large() any { return "hello" }`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(mod)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		limit int
		want  string
	}{
		{"Result", 1024, `{"rows":[false,-4,null,9223372036854775809,"\n"],"empty":[],"array":[1.25,2]}`},
		{"Cycle", 8192, "cyclic"}, {"Bad", 64, "keys must be strings"}, {"Large", 4, "byte limit"},
	} {
		var res [1]values.Value
		if err := evmtest.New(p).Run(context.Background(), tc.name, nil, res[:]); err != nil {
			t.Fatal(err)
		}
		raw, err := p.EncodeJSON(context.Background(), res[0], tc.limit)
		direct, directErr := p.JSONValue(context.Background(), res[0], tc.limit)
		if tc.name != "Result" {
			if err == nil || !strings.Contains(err.Error(), tc.want) || directErr == nil || !strings.Contains(directErr.Error(), tc.want) {
				t.Fatalf("%s: %s %v", tc.name, raw, err)
			}
			continue
		}
		decode := func(raw string) any {
			d := json.NewDecoder(strings.NewReader(raw))
			d.UseNumber()
			var v any
			if err := d.Decode(&v); err != nil {
				t.Fatal(err)
			}
			return v
		}
		if err != nil || !reflect.DeepEqual(decode(tc.want), decode(string(raw))) {
			t.Fatalf("%s: %s %v", tc.name, raw, err)
		}
		if directErr != nil || !reflect.DeepEqual(direct, decode(string(raw))) {
			t.Fatalf("direct JSON: %#v, %v", direct, directErr)
		}
	}
}

func TestJSONValueStringsAndLimits(t *testing.T) {
	m, err := evmcompiler.Compile("json.go", `func Keep(s string) any { return s }`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"", "hello", "\"\\\b\f\n\r\t\x00<>&\u2028\u2029", "😀\ufffd", "\xff\xfe\xe0\x80\x80"} {
		t.Run(fmt.Sprintf("%q", s), func(t *testing.T) {
			raw, _ := json.Marshal(s)
			v, err := p.PackInterface(values.StringType, values.String(s))
			if err != nil {
				t.Fatal(err)
			}
			var want any
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			for _, limit := range []int{len(raw) - 1, len(raw), len(raw) + 1} {
				out, err := p.JSONValue(context.Background(), v, limit)
				if limit < len(raw) {
					if err == nil {
						t.Fatal("size accepted", limit)
					}
					continue
				}
				if err != nil || out != want {
					t.Fatalf("%#v %v, want %#v (raw %s, limit %d)", out, err, want, raw, limit)
				}
			}
		})
	}
	data := []byte("borrowed")
	v, _ := p.PackInterface(values.StringType, values.Bytes(data))
	out, err := p.JSONValue(context.Background(), v, 64)
	if err != nil {
		t.Fatal(err)
	}
	clear(data)
	if out != "borrowed" {
		t.Fatal("retained string still borrows input")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.JSONValue(ctx, v, 64); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestJSONValueAllocationBudget(t *testing.T) {
	called := false
	pkg := evm.Package{Path: "host", Functions: []evm.ExternalFunction{{Name: "Copy", Signature: evm.Signature{Params: []*values.Type{values.AnyType}}, Call: func(c *evm.VM) error {
		called = true
		_, err := c.JSONValue(c.CallArgument(0), 1<<20)
		return err
	}}}}
	m, err := evmcompiler.Compile("json.go", `import "host"; func Copy(v any) { host.Copy(v) }; func Keep(s string) any { return s }`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, pkg)
	if err != nil {
		t.Fatal(err)
	}
	vm := evmtest.New(p)
	v, _ := p.PackInterface(values.StringType, values.String("warm"))
	if err := vm.Run(context.Background(), "Copy", []values.Value{v}, nil); err != nil {
		t.Fatal(err)
	}
	v, _ = p.PackInterface(values.StringType, values.String(strings.Repeat("x", 1024)))
	called = false
	err = vm.RunWithBudget(context.Background(), "Copy", []values.Value{v}, nil, evm.Budget{AllocationBytes: 64})
	if !called || !errors.Is(err, evm.ErrAllocation) {
		t.Fatalf("called=%v: %v", called, err)
	}
}

func TestJSONInterfaceHeaders(t *testing.T) {
	m, err := evmcompiler.Compile("json.go", `
func Keep(b bool, i int64, u uint64, f float64, s string, a []any, m map[string]any) any {
 return []any{b, i, u, f, s, a, m}
}
func Check(v any) bool {
 a := v.([]any)
 return !a[0].(bool) && a[1].(int64) == -9223372036854775808 &&
 a[2].(uint64) == 18446744073709551615 && a[4].(string) == "retained" &&
 a[5] != nil && a[5].([]any) == nil && a[6] != nil && a[6].(map[string]any) == nil
}`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m)
	if err != nil {
		t.Fatal(err)
	}
	args := []values.Value{values.Bool(false), values.Int(math.MinInt64), values.Uint(math.MaxUint64), values.Uint(0x7ff8000000000042), values.String(strings.Clone("retained")), {}, {}}
	var out [1]values.Value
	vm := evmtest.New(p)
	if err := vm.Run(context.Background(), "Keep", args, out[:]); err != nil {
		t.Fatal(err)
	}
	s, payload, err := p.UnpackInterface(out[0])
	if err != nil || s.Kind != values.SliceKind {
		t.Fatalf("slice: %v %v", s, err)
	}
	items := values.CellSlice(payload.P, uint64(payload.SliceLen()))
	for i, typ := range []*values.Type{values.Boolean, values.Int64, values.Uint64, values.Float64, values.StringType} {
		t.Run(typ.Name(), func(t *testing.T) {
			if n := testing.AllocsPerRun(100, func() {
				v, err := p.PackInterface(typ, args[i])
				if err != nil {
					t.Fatal(err)
				}
				s, x, err := p.UnpackInterface(v)
				if err != nil || s.Type != typ || x != args[i] {
					t.Fatalf("round trip: %v %v %v", s, x, err)
				}
				items[i] = v
			}); n != 0 {
				t.Fatalf("interface header allocated %g times", n)
			}
		})
	}
	for _, item := range append(items[5:], out[0]) {
		s, payload, err := p.UnpackInterface(item)
		if err != nil {
			t.Fatal(err)
		}
		if n := testing.AllocsPerRun(100, func() {
			v, err := p.PackInterface(s.Type, payload)
			if err != nil {
				t.Fatal(err)
			}
			typ, got, err := p.UnpackInterface(v)
			if err != nil || typ.Type != s.Type || got != payload {
				t.Fatalf("collection round trip: %v %v %v", typ, got, err)
			}
		}); n != 0 {
			t.Fatalf("collection interface header allocated %g times", n)
		}
	}
	runtime.GC()
	if err := vm.Run(context.Background(), "Check", out[:], out[:]); err != nil || out[0].R != 1 {
		t.Fatalf("retained JSON values: %v %v", out, err)
	}
}

func TestJSONValueWithSize(t *testing.T) {
	seen := 0
	bridge := evm.Package{Path: "bridge", Functions: []evm.ExternalFunction{{Name: "Check", Signature: evm.Signature{Params: []*values.Type{values.AnyType}}, Call: func(c *evm.VM) error {
		v := c.CallArgument(0)
		raw, err := c.EncodeJSON(v, 1<<20)
		if err != nil {
			return err
		}
		out, size, err := c.JSONValueWithSize(v, len(raw))
		if err != nil {
			return err
		}
		if size != len(raw) {
			return fmt.Errorf("materialized size %d, encoded size %d", size, len(raw))
		}
		expected, err := c.JSONValue(v, len(raw))
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(out, expected) {
			return fmt.Errorf("materialization changed: %v != %v", out, expected)
		}
		if _, _, err := c.JSONValueWithSize(v, len(raw)-1); err == nil {
			return errors.New("undersized output limit accepted")
		}
		seen++
		return nil
	}}}}
	module, err := evmcompiler.Compile("sizes.go", `import "bridge"
 func Run(){
 bridge.Check(nil)
 bridge.Check("\n\t\"<>&")
 bridge.Check(map[string]any{"rows":[]any{int64(-7),uint64(1<<63)+1,true,nil},"nested":map[string]any{"text":"value"}})
 }`, bridge)
	if err != nil {
		t.Fatal(err)
	}
	program, err := evm.Link(module, bridge)
	if err != nil {
		t.Fatal(err)
	}
	if err := evmtest.New(program).Run(context.Background(), "Run", nil, nil); err != nil {
		t.Fatal(err)
	}
	if seen != 3 {
		t.Fatalf("converted %d values", seen)
	}
}

func TestGuestValueNativeJSONBoundary(t *testing.T) {
	mod, err := evmcompiler.Compile("output.go", `type Label string
 type Local struct{ Name string }
 func LabelOnly()any{return Label("gold")}
 func Good()any{return map[string]any{"name":Label("gold"),"count":uint64(18446744073709551615)}}
 func Bad()any{return &Local{Name:"unsupported"}}
 func Cycle()any{m:=map[string]any{};m["self"]=m;return m}`)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(mod)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Good", "Bad", "Cycle", "LabelOnly"} {
		var results [1]values.Value
		if err := evmtest.New(p).Run(context.Background(), name, nil, results[:]); err != nil {
			t.Fatal(err)
		}
		guest := &evm.GuestValue{Program: p, Value: results[0]}
		raw, err := json.Marshal(map[string]any{"nested": guest})
		if name == "LabelOnly" {
			if err != nil || string(raw) != `{"nested":"gold"}` || fmt.Sprint(guest) != "gold" {
				t.Fatalf("label: %s %v %s", raw, err, guest)
			}
			continue
		}
		if name == "Good" {
			if got := fmt.Sprint(guest); got != "map[count:18446744073709551615 name:gold]" {
				t.Fatalf("formatted: %s", got)
			}
			if err != nil {
				t.Fatal(err)
			}
			var actual map[string]json.RawMessage
			if err := json.Unmarshal(raw, &actual); err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(actual["nested"], &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["name"]) != `"gold"` || string(fields["count"]) != "18446744073709551615" {
				t.Fatalf("unexpected output: %s", raw)
			}
		} else if !strings.Contains(fmt.Sprint(guest), "<EVM value:") {
			t.Fatalf("unsupported formatting: %s", guest)
		} else if err == nil {
			t.Fatalf("%s serialized unsupported value: %s", name, raw)
		}
	}
	var absent *evm.GuestValue
	raw, err := json.Marshal(absent)
	if err != nil || string(raw) != "null" {
		t.Fatalf("nil: %s %v", raw, err)
	}
}
