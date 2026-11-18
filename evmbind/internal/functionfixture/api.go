package functionfixture

import "github.com/andreyvit/evm"

func Register(fn *evm.NamedFunctionOf[func(int) int])           { panic("host") }
func RegisterText(fn *evm.NamedFunctionOf[func(string) string]) { panic("host") }

func RegisterValue(fn *evm.NamedFunctionOf[func(int) any]) { panic("host") }

func Keep(fn *evm.FunctionOf[func(int) int])         { panic("host") }
func Get() *evm.FunctionOf[func(int) int]            { panic("host") }
func GetWrong() *evm.FunctionOf[func(string) string] { panic("host") }

type FunctionSlot struct {
	Fn *evm.FunctionOf[func(int) int]
}

func Slot() *FunctionSlot                  { panic("host") }
func EchoSlot(s FunctionSlot) FunctionSlot { panic("host") }

func RegisterRows(fn *evm.NamedFunctionOf[func(func([]string), func([]any))]) { panic("host") }

func Strings(values []string) { panic("host") }

type NativeUnary func(int) int
type NativeBox struct{ Apply NativeUnary }

func NativeSlot() *NativeBox                { panic("host") }
func EchoNative(fn NativeUnary) NativeUnary { panic("host") }

func KeepAny(fn *evm.FunctionOf[any]) { panic("host") }
