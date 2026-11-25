package evmbind_test

import (
	"context"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmcompiler"
	"github.com/andreyvit/evm/evmtest"
	"github.com/andreyvit/evm/values"
)

type Wallet struct {
	Owner string
	Coin  Coin
	Spare *Coin
}

type walletHost struct{ w *Wallet }

func (h *walletHost) Open(*evm.VM) *Wallet          { return h.w }
func (h *walletHost) Sum(_ *evm.VM, w Wallet) int64 { return w.Coin.Amount*100 + int64(w.Coin.Code) }
func (h *walletHost) Make(_ *evm.VM, c Coin) Wallet { return Wallet{Owner: "made", Coin: c} }
func (h *walletHost) Coins(*evm.VM) []Coin          { return []Coin{{1, 2}, {3, 4}} }

// A compact struct keeps its Go layout inside a native parent struct, while a
// copy of it is one packed cell. Every way to reach the field must convert
// between the two.
func TestCompactStructInNativeStruct(t *testing.T) {
	native, err := evmbind.New("money", fstest.MapFS{"money.go": {Data: []byte(coinSource)}}, evmbind.Options{
		Only: []string{"Coin"}, NativePackage: reflect.TypeFor[Coin]().PkgPath(), Types: []reflect.Type{reflect.TypeFor[Coin]()},
		CellStructs: map[string]*values.CellStruct{"Coin": coinCell()},
	})
	if err != nil {
		t.Fatal(err)
	}
	money := evm.Package{Path: "money", Source: coinSource}
	for _, d := range native.Package().Types {
		if d.Name == "Coin" {
			d.Name, d.SourceName = "", "Coin"
		}
		money.Types = append(money.Types, d)
	}
	h := &walletHost{}
	host, err := evmbind.New("host", fstest.MapFS{"host.go": {Data: []byte(`package host
import "native/money"
type Wallet struct { Owner string; Coin money.Coin; Spare *money.Coin }
func Open() *Wallet
func Sum(w Wallet) int64
func Make(c money.Coin) Wallet
func Coins() []money.Coin
`)}}, evmbind.Options{Imports: map[string]*evmbind.Schema{"native/money": native}, Host: reflect.TypeFor[*walletHost](), NativePackage: reflect.TypeFor[Wallet]().PkgPath(), Types: []reflect.Type{reflect.TypeFor[Wallet]()}})
	if err != nil {
		t.Fatal(err)
	}
	pkgs := []evm.Package{money, host.Bind(h)}
	for _, tc := range []struct {
		name, body string
		want       int64
		coin       Coin // the host's wallet afterwards
	}{
		{"field of the field", `w := host.Open(); return w.Coin.Amount*100 + int64(w.Coin.Code)`, 507, Coin{5, 7}},
		{"address of the field", `w := host.Open(); p := &w.Coin; return p.Amount*100 + int64(p.Code)`, 507, Coin{5, 7}},
		{"whole field, then a source method", `w := host.Open(); if !w.Coin.Valid() { return -1 }; return w.Coin.Add(w.Coin).Amount`, 10, Coin{5, 7}},
		{"copy of the parent to the host", `w := *host.Open(); return host.Sum(w)`, 507, Coin{5, 7}},
		{"store into the field", `w := host.Open(); w.Coin = money.Make(9, 3); return host.Sum(*w)`, 903, Coin{9, 3}},
		{"parent returned by the host", `w := host.Make(money.Make(4, 6)); return w.Coin.Amount*100 + int64(w.Coin.Code)`, 406, Coin{5, 7}},
		{"parent built by the script", `return host.Sum(host.Wallet{Owner: "x", Coin: money.Make(2, 8)})`, 208, Coin{5, 7}},
		{"pointer field", `w := host.Open(); c := money.Make(6, 1); w.Spare = &c; return w.Spare.Amount*100 + int64(w.Spare.Code)`, 601, Coin{5, 7}},
		{"slice element", `c := host.Coins(); return c[1].Amount*100 + int64(c[1].Code)`, 304, Coin{5, 7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h.w = &Wallet{Owner: "ann", Coin: Coin{5, 7}}
			m, err := evmcompiler.Compile("run.go", "import(\"host\";\"money\")\nvar _ = money.Make\nfunc Run() int64 { "+tc.body+" }", pkgs...)
			if err != nil {
				t.Fatal(err)
			}
			p, err := evm.Link(m, pkgs...)
			if err != nil {
				t.Fatal(err)
			}
			var out [1]values.Value
			if err = evmtest.New(p).Run(context.Background(), "Run", nil, out[:]); err != nil || out[0].AsInt() != tc.want {
				t.Fatalf("got %d, wanted %d (%v)", out[0].AsInt(), tc.want, err)
			}
			if h.w.Coin != tc.coin || h.w.Owner != "ann" {
				t.Fatalf("host wallet is %+v", *h.w)
			}
		})
	}
}
