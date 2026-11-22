package functionfixture

import "github.com/andreyvit/evm"

func Register(fn *evm.FunctionOf[func(int) int])           { panic("host") }
func RegisterText(fn *evm.FunctionOf[func(string) string]) { panic("host") }

func RegisterValue(fn *evm.FunctionOf[func(int) any]) { panic("host") }

func Keep(fn *evm.FunctionOf[func(int) int])         { panic("host") }
func Get() *evm.FunctionOf[func(int) int]            { panic("host") }
func GetWrong() *evm.FunctionOf[func(string) string] { panic("host") }

type FunctionSlot struct {
	Fn *evm.FunctionOf[func(int) int]
}

func Slot() *FunctionSlot                  { panic("host") }
func EchoSlot(s FunctionSlot) FunctionSlot { panic("host") }

func RegisterRows(fn *evm.FunctionOf[func(func([]string), func([]any))]) { panic("host") }

func Strings(values []string) { panic("host") }

type NativeUnary func(int) int
type NativeBox struct{ Apply NativeUnary }

func NativeSlot() *NativeBox                { panic("host") }
func EchoNative(fn NativeUnary) NativeUnary { panic("host") }

func KeepAny(fn *evm.FunctionOf[any]) { panic("host") }

//evm:borrow-callback-bytes fixture.rows visit
func (*NativeBox) Visit(data []byte, visit func([]byte)) { visit(data) }

//evm:borrow-yield-bytes fixture.rows
func (*NativeBox) Bytes(data []byte) func(func([]byte) bool) {
	return func(yield func([]byte) bool) { yield(data) }
}

func (*NativeBox) Writer() func([]byte) {
	return func(data []byte) { data[0]++ }
}
