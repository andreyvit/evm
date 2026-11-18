//go:build !evmreflect

package evmbind_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmbind/internal/packagefunctionbindings"
	"github.com/andreyvit/evm/evmbind/internal/packagefunctionfixture"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/values"
)

type Bounds func(int) int

func TestGeneratedNativePackageFunctions(t *testing.T) {
	generated, err := evmbind.Generate("functions", "github.com/andreyvit/evm/evmbind/internal/packagefunctionfixture", "packagefunctionbindings", packagefunctionfixture.Source, packagefunctionfixture.Options())
	if err != nil {
		t.Fatal(err)
	}
	existing, err := os.ReadFile("internal/packagefunctionbindings/native_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, existing) {
		t.Fatal("regenerate package function bindings")
	}
	for _, static := range []bool{false, true} {
		t.Run(map[bool]string{false: "reflection", true: "generated"}[static], func(t *testing.T) {
			opt := packagefunctionfixture.Options()
			opt.Functions["Double"] = Bounds(func(n int) int { return n + 9 })
			if static {
				opt.Mode = evmbind.StaticOnly
				opt.Static = &packagefunctionbindings.Bindings
			}
			schema, err := evmbind.New("functions", packagefunctionfixture.Source, opt)
			if err != nil {
				t.Fatal(err)
			}
			if schema.UsesStatic() != static {
				t.Fatal("wrong binding mode")
			}
			pkg := schema.Bind(nil)
			module, err := evmcompiler.Compile("functions.go", `import f "functions"
  func Run()bool{
   if f.Sum(3)!=3 || f.Sum(3,4,5)!=12{return false}
   nums:=[]int{1,2};f.SetFirst(nums...);if nums[0]!=42{return false}
   var absent []int;if f.Sum(9,absent...)!=9{return false}
   counter:=f.NewCounter(1)
   if counter.Add()!=1 || counter.Add(2,3)!=6{return false}
   adder:=f.AsAdder(counter)
   if adder.Add(4,5)!=15 || adder.Add(absent...)!=15{return false}
   add:=counter.Add;if add(1,2)!=18{return false}
   expr:=(*f.Counter).Add;if expr(counter,1,2)!=21{return false}
   func(){defer counter.Add(3,4)}();if counter.Value!=28{return false}
   ifaceAdd:=adder.Add;if ifaceAdd(1,2)!=31{return false}
   group:=f.NewGroup(counter,counter)
   group.First=group.Children[0];group.Named=map[string]f.Adder{"first":group.First}
   if group.Named["first"]!=counter || group.Children[1].Add()!=31{return false}
   group.Children[0]=nil;if group.First!=counter || group.Children[0]!=nil{return false}
   c:=f.NewCounter(3);if f.Increment(c,4)!=7 || c.Value!=7 || f.Double(5)!=14{return false}
   if f.Visit(c,func(n int)bool{return n==7})!=8{return false}
   n,err:=f.Split([]int{42});if n!=42 || err!=nil{return false}
   _,err=f.Split(nil);if err==nil || err.Error()!="empty"{return false}
   f.Reset(c);if c.Value!=0{return false}
   lower:=[]byte("a");bounds:=f.RangeOf(lower,[]byte("z"));lower[0]='b'
   return string(bounds.Lower)=="b" && string(bounds.Upper)=="z"
  }`, pkg)
			if err != nil {
				t.Fatal(err)
			}
			program, err := evm.Link(module, pkg)
			if err != nil {
				t.Fatal(err)
			}
			var out [1]values.Value
			if err := evm.New(program).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].R != 1 {
				t.Fatal(out, err)
			}
		})
	}
}
