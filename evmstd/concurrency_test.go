package evmstd_test

import (
	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmstd"
	"github.com/andreyvit/evm/values"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

// registration models a host registration parameter that accepts any signature.
func registration() evm.Package {
	return evm.Package{Path: "reg", ConcurrentCallbackParams: map[string][]int{"AddHook": {0}}, Functions: []evm.ExternalFunction{{Name: "AddHook", Signature: evm.Signature{Params: []*values.Type{values.AnyType}}, Call: func(vm *evm.VM) error { return nil }}}}
}

func TestSharedCache(t *testing.T) {
	got := run(t, `import("evmruntime/concurrency";"time")
 type key struct{site int;id string}
 type Client struct { tokens *concurrency.Cache[key,string] }
 func(c *Client) Read()string{
  var exp time.Time
  k:=key{3,"a"}
  e,err:=c.tokens.GetOrLoad(k,func()(string,time.Time,error){return "token",exp,nil})
  if err!=nil {panic(err)}
  c.tokens.Set(k,"new",exp)
  c.tokens.InvalidateIfCurrent(k,e)
  e,_=c.tokens.Get(k)
  return e.Value
 }
 func Run()string{c:=&Client{concurrency.NewCache[key,string]()};f:=c.Read;return f()}`)
	if got != "new" {
		t.Fatal(got)
	}
}

func TestConcurrentCallbackVerification(t *testing.T) {
	for _, tc := range []struct {
		code string
		bad  bool
	}{
		{"func Run(s []int){reg.AddHook(func(){p:=&s[0];*p=2})}", true},
		{"func Run(s []int){reg.AddHook(func(){_=append(s,1)})}", true},
		{"func Run(m map[int]int){reg.AddHook(func(){delete(m,0)})}", true},
		{"func Run(s []*int){reg.AddHook(func(){for _,p:=range s{*p=2}})}", true},
		{"func Run(m map[int]*int){reg.AddHook(func(){for _,p:=range m{*p=2}})}", true},
		{"func Run(c *C){reg.AddHook(func(){f:=func(){c.n++};f()})}", true},
		{"func Run(c *C){reg.AddHook(func()map[int]int{return c.m})}", false},
		{"func pick(c *C)map[int]int{return c.m};func Run(c *C){reg.AddHook(func()int{return pick(c)[0]})}", false},
		{"func pick(c *C)map[int]int{return c.m};func Run(c *C){reg.AddHook(func(){pick(c)[0]=1})}", true},
		{"func Run(c *C){reg.AddHook(func(){pick:=func()map[int]int{return c.m};pick()[0]=1})}", true},
		{"func Run(c *C){reg.AddHook(func()int{a:=map[int]int{};a[0]=c.n;return a[0]})}", false},
		{"func Run(c *C){reg.AddHook(func()int{n:=0;for _,v:=range c.m{n+=v};return n})}", false},
		{"func(c *C) F(){c.n++};func Run(c *C){reg.AddHook(c.F)}", true},
		{"func(c *C) F(){c.m[0]++};func Run(c *C){reg.AddHook(c.F)}", true},
		{"func change(p *int){*p++};func(c *C) F(){change(&c.n)};func Run(c *C){reg.AddHook(c.F)}", true},
		{"func Run(){n:=0;reg.AddHook(func(){n++})}", true},
		{"func Run(m map[int]int){reg.AddHook(func(){a:=m;a[1]=2})}", true},
		{"func Run(c *C){reg.AddHook(func(m map[int]int){m[0]=c.n})}", false},
		{"func(c *C) F(){c.n++};func Run(c *C){_=reg.AddHook;_=c.F}", false},
	} {
		_, err := evmcompiler.Compile("shared.go", `import "reg";type C struct{n int;m map[int]int};`+tc.code, registration())
		if (err != nil) != tc.bad || (err != nil && !strings.Contains(err.Error(), "concurrent callback")) {
			t.Errorf("%s: %v", tc.code, err)
		}
	}
}

func TestCacheSnapshots(t *testing.T) {
	got := run(t, `import("evmruntime/concurrency";"time")
 type record struct{Text string;Parts [2]int}
 func Run() string {
  var exp time.Time
  c:=concurrency.NewCache[string,record]()
  r:=record{"original",[2]int{1,2}}
  c.Set("a",r,exp)
  r.Text="input changed"
  a,_:=c.Get("a")
  a.Value.Text="output changed"
  a.Value.Parts[0]=3
  b,_:=c.Get("a")
  if b.Value.Parts[0]!=1 {panic("shared mutable result")}
  return b.Value.Text
 }`)
	if got != "original" {
		t.Fatal(got)
	}
}

func TestUnsupportedCacheTypes(t *testing.T) {
	for _, args := range []string{"string,map[string]int", "*int,int", "[2]*int,int", "float64,string"} {
		_, err := evmcompiler.Compile("cache.go", `import "evmruntime/concurrency";func Run(){_=concurrency.NewCache[`+args+`]()}`, evmstd.Packages()...)
		if err == nil || !strings.Contains(err.Error(), "cannot share") {
			t.Errorf("NewCache[%s]: %v", args, err)
		}
	}
}

func TestRegisteredSharedMethod(t *testing.T) {
	packages := append(evmstd.Packages(), registration())
	var loads atomic.Int32
	var release chan struct{}
	packages = append(packages, evm.Package{Path: "service", Functions: []evm.ExternalFunction{
		{Name: "Fetch", Signature: evm.Signature{Params: []*values.Type{values.StringType}, Results: []*values.Type{values.StringType}}, Call: func(vm *evm.VM) error {
			loads.Add(1)
			<-release
			vm.SetCallResult(0, vm.CallArgument(0))
			return nil
		}},
	}})
	m, err := evmcompiler.Compile("plugin.go", `import ("evmruntime/concurrency";"time";"service";"reg")
 type Client struct{ cache *concurrency.Cache[string,string]; prefix string }
 func(c *Client) Handle(id string) string {
  e,err:=c.cache.GetOrLoad("token",func()(string,time.Time,error){var exp time.Time;return service.Fetch(c.prefix+id),exp,nil})
  if err!=nil{panic(err)}
  return e.Value
 }
 func Initialize()func(string)string{
  c:=&Client{concurrency.NewCache[string,string](),"order-"}
  f:=c.Handle;reg.AddHook(f);return f
 }`, packages...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, packages...)
	if err != nil {
		t.Fatal(err)
	}
	init, err := p.LookupFunction("Initialize", nil)
	if err != nil {
		t.Fatal(err)
	}
	vm := p.AcquireVM(t.Context())
	var slot [1]values.Value
	err = vm.Run(init, slot[:])
	vm.Release()
	if err != nil {
		t.Fatal(err)
	}
	fn, err := p.Callable(slot[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := fn.CheckConcurrentUse(); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		release = make(chan struct{})
		var wg sync.WaitGroup
		for range 12 {
			wg.Go(func() {
				vm := p.AcquireVM(t.Context())
				defer vm.Release()
				slots := [2]values.Value{values.String("123")}
				if err := vm.Run(fn, slots[:]); err != nil {
					t.Error(err)
				} else if got := slots[1].AsString(); got != "order-123" {
					t.Error(got)
				}
			})
		}
		synctest.Wait()
		close(release)
		wg.Wait()
	})
	if loads.Load() != 1 {
		t.Fatal(loads.Load(), "refreshes")
	}
}

func TestArrayCache(t *testing.T) {
	got := run(t, `import("evmruntime/concurrency";"time")
 func Run()string{
  c:=concurrency.NewCache[[2]int,[1]string]()
  k:=[2]int{3,4};var exp time.Time
  c.Set(k,[1]string{"ok"},exp)
  e,_:=c.Get(k)
  return e.Value[0]
 }`)
	if got != "ok" {
		t.Fatal(got)
	}
}

func TestRegistrationRequiresCapturesVerifiedForConcurrency(t *testing.T) {
	packages := append(evmstd.Packages(), registration())
	m, err := evmcompiler.Compile("verify.go", `import "reg"
 type C struct { n int }
 func(c *C) Read() int{return c.n}
 func Plain() int{return 1}
 func Build() (func()int,func()int,func()int,func()int){
  c:=&C{3};n:=5
  a:=c.Read;b:=func()int{return n};reg.AddHook(a);reg.AddHook(b);other:=0;return a,b,func()int{other++;return other},Plain
 }`, packages...)
	if err != nil {
		t.Fatal(err)
	}
	p, err := evm.Link(m, packages...)
	if err != nil {
		t.Fatal(err)
	}
	fn, err := p.LookupFunction("Build", nil)
	if err != nil {
		t.Fatal(err)
	}
	vm := p.AcquireVM(t.Context())
	defer vm.Release()
	var out [4]values.Value
	if err := vm.Run(fn, out[:]); err != nil {
		t.Fatal(err)
	}
	for i, v := range out {
		fn, err := p.Callable(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := fn.CheckConcurrentUse(); (err != nil) != (i == 2) {
			t.Errorf("callback %d: %v", i, err)
		}
	}
}

func TestCacheLoaderErrorAndNestedMiss(t *testing.T) {
	got := run(t, `import("evmruntime/concurrency";"errors";"time")
 func Run()string{
  c:=concurrency.NewCache[string,string]()
  want:=errors.New("unavailable")
  var exp time.Time
  _,err:=c.GetOrLoad("x",func()(string,time.Time,error){return "",exp,want})
  if err!=want {panic("lost loader error")}
  _,found:=c.Get("x");if found{panic("stored failure")}
  c.Set("hit","ok",exp)
  entry,err:=c.GetOrLoad("x",func()(string,time.Time,error){
   f:=func()(string,time.Time,error){return "unexpected",exp,nil}
   _,err:=c.GetOrLoad("x",f)
   if err==nil {panic("recursive miss")}
   hit,err:=c.GetOrLoad("hit",f)
   return hit.Value,exp,err
  })
  if err!=nil{panic(err)}
  return entry.Value
 }`)
	if got != "ok" {
		t.Fatal(got)
	}
}

func TestCacheLoaderPanic(t *testing.T) {
	got := run(t, `import("evmruntime/concurrency";"time")
 func attempt(c *concurrency.Cache[string,string])(p any){
  defer func(){p=recover()}()
  c.GetOrLoad("key",func()(string,time.Time,error){panic("loader panic")})
  return nil
 }
 func Run()string{
  c:=concurrency.NewCache[string,string]()
  if attempt(c)!="loader panic"{panic("lost guest panic")}
  e,err:=c.GetOrLoad("key",func()(string,time.Time,error){var exp time.Time;return "retry",exp,nil})
  if err!=nil{panic(err)}
  return e.Value
 }`)
	if got != "retry" {
		t.Fatal(got)
	}
}

func TestConcurrentBorrow(t *testing.T) {
	const prefix = `import "reg"
type C struct { m map[string]int }
func (c *C) F() int { return c.m["x"] }
func mutate(c *C) { c.m["x"]++ }
func publish(c *C) { reg.AddHook(c.F) }
func both(a,b *C) { reg.AddHook(a.F); b.m["x"]++ }
type list struct { items []*C }
func (l *list) Count() int { return len(l.items) }
type reader struct { read func() int }
func (r *reader) Ready() bool { return r.read != nil }
var saved *C
func fresh() *C { return &C{map[string]int{}} }
func savedClient() *C { saved = fresh(); return saved }
func field(c *C) map[string]int { return c.m }
var savedMap map[string]int
func prepare(c *C) { savedMap = field(c) }
func Run(flag bool) {
    c:=&C{map[string]int{}}
    alias:=c.m
    _=alias
`
	for _, tc := range []struct {
		body string
		bad  bool
	}{
		{`alias["x"]=1;reg.AddHook(c.F);_=alias["x"]`, false},
		{`if flag {reg.AddHook(c.F)} else {alias["x"]++}`, false},
		{`if flag {reg.AddHook(c.F)};alias["x"]++`, true},
		{`f:=c.F;alias["x"]++;reg.AddHook(f)`, true},
		{`both(c,c)`, true},
		{`both(c,&C{map[string]int{}})`, false},
		{`inner:=func(){reg.AddHook(c.F)};inner();alias["x"]++`, true},
		{`publish(c);c.m["x"]++`, true},
		{`reg.AddHook(c.F);mutate(c)`, true},
		{`reg.AddHook(c.F);reg.AddHook(c.F)`, false},
		{`f:=c.F;alias["x"]++;_=f()`, false},
		{`getter:=func()map[string]int{return c.m};m:=getter();reg.AddHook(c.F);m["x"]++`, true},
		{`defer mutate(c);reg.AddHook(c.F)`, true},
		{`for flag {alias["x"]++;reg.AddHook(c.F)}`, true},
		{`reg.AddHook(c.F);other:=&C{map[string]int{}};other.m["x"]++`, false},
		{`l:=&list{make([]*C,1)};copy(l.items,[]*C{c});reg.AddHook(l.Count);mutate(c)`, true},
		{`dst:=[]int{0};src:=[]int{1};copy(dst,src);reg.AddHook(func()int{return dst[0]});src[0]++`, false},
		{`r:=&reader{c.F};reg.AddHook(r.Ready);mutate(c)`, true},
		{`r:=&reader{c.F};reg.AddHook(r.Ready);_=c.m["x"]`, false},
		{`v:=fresh();v.m["x"]++;reg.AddHook(v.F)`, false},
		{`v:=savedClient();reg.AddHook(v.F)`, true},
		{`setup:=func(){saved=c};setup();reg.AddHook(c.F)`, true},
		{`setup:=func(){c.m["x"]=1};setup();reg.AddHook(c.F)`, false},
		{`prepare(c);reg.AddHook(c.F)`, true},
		{`reg.AddHook(c.F);panic(c)`, true},
		{`reg.AddHook(c.F);panic("setup failed")`, false},
	} {
		_, err := evmcompiler.Compile("borrow.go", prefix+tc.body+"}", registration())
		if (err != nil) != tc.bad || (err != nil && !strings.Contains(err.Error(), "concurrent")) {
			t.Errorf("%s: %v", tc.body, err)
		}
	}
}
