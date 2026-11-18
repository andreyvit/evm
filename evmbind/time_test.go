package evmbind_test

import (
	"context"
	"reflect"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/timedata"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

func TestNativeTimeSource(t *testing.T) { testNativeTimeSource(t, nil) }
func testNativeTimeSource(t *testing.T, static *evmbind.Static) {
	t.Helper()
	opt := evmbind.Options{Methods: map[string][]string{"Context": {"Echo", "Pointer"}}, Types: []reflect.Type{reflect.TypeFor[timedata.Context]()}, Static: static}
	if static != nil {
		opt.Mode = evmbind.StaticOnly
	}
	schema, err := evmbind.NewTypes("host/timecontext", timedata.Source, nil, opt)
	if err != nil {
		t.Fatal(err)
	}
	pkg := schema.Package()
	mod, err := evmcompiler.Compile("check.go", `import ("host/timecontext";"time")
 func Run(c *timecontext.Context) string {
  c.Locals[0].Label="local type"
  original:=c.At
  c.List[0]=original
  c.Named[original]="present"
  if c.Named[c.List[0]]!="present" {panic("native time collection")}
  fresh:=[]time.Time{original}
  c.List=fresh
  c.Named=map[time.Time]string{original:"present"}
  c.At=c.Echo(original)
  ptr:=&c.At
  if c.Pointer(ptr)!=ptr {panic("time pointer identity")}
  *ptr=original
  local:=new(time.Time)
  *local=original
  if *c.Pointer(local)!=original {panic("guest time allocation")}
  c.At=time.Time{}
  if !c.At.IsZero() {panic("zero store")}
  c.At=original
  if c.At!=original { panic("native timestamp changed") }
  return c.At.Format("15:04 -0700 MST")
 }`, pkg)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(mod, schema.Bind(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []time.Time{time.Time{}, time.Unix(123, 456).UTC(), time.Date(2500, 1, 1, 0, 0, 0, 1, time.UTC), time.Now(), time.Date(2026, 11, 14, 18, 0, 0, 123, time.FixedZone("shop", 14400))} {
		c := &timedata.Context{Locals: []timedata.Time{{}}, At: stamp, List: make([]time.Time, 1), Named: make(map[time.Time]string)}
		arg, err := schema.NativePointer(c)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]values.Value, 1)
		if err := evm.New(p).Run(context.Background(), "Run", []values.Value{arg}, out); err != nil {
			t.Fatal(err)
		}
		if c.Locals[0].Label != "local type" || c.List[0] != stamp || c.Named[stamp] != "present" || c.At != stamp || out[0].AsString() != stamp.Format("15:04 -0700 MST") {
			t.Fatal("native time changed", c.At, stamp, out)
		}
	}
}

func TestImportedNativeTimeGeneration(t *testing.T) {
	schema, err := evmbind.NewTypes("host/timecontext", timedata.Source, nil, evmbind.Options{Types: []reflect.Type{reflect.TypeFor[timedata.Context]()}})
	if err != nil {
		t.Fatal(err)
	}
	source := fstest.MapFS{"api.go": {Data: []byte(`package host; import "github.com/andreyvit/evm/evmbind/internal/timedata"; func Context() *timedata.Context`)}}
	if _, err := evmbind.GenerateWithImports("host", "host", "bindings", source, map[string]*evmbind.Schema{"github.com/andreyvit/evm/evmbind/internal/timedata": schema}); err != nil {
		t.Fatal(err)
	}
}
