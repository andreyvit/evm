package modelhost

import "github.com/andreyvit/evm/evmbind/internal/modeldata"
import "time"
import "github.com/andreyvit/evm"

func Get() *modeldata.BoundModel                         { panic("declaration") }
func Echo(r *modeldata.BoundModel) *modeldata.BoundModel { panic("declaration") }
func Copy(r modeldata.BoundModel) modeldata.BoundModel   { panic("declaration") }
func Same(r *modeldata.BoundModel) bool                  { panic("declaration") }

func Other() *modeldata.Alternate { panic("declaration") }

func Pair(a, b *modeldata.BoundModel) bool { panic("declaration") }

type genericAlias = modeldata.Generic[string]
type stringAlias = string

func GenericGet() *modeldata.Generic[stringAlias]                       { panic("declaration") }
func GenericEcho(r *genericAlias) *genericAlias                         { panic("declaration") }
func GenericCopy(r modeldata.Generic[string]) modeldata.Generic[string] { panic("declaration") }

func GenericNamed() *modeldata.Generic[modeldata.BoundModel] { panic("declaration") }

func GenericDuration() *modeldata.Generic[time.Duration] { panic("declaration") }

func Rows() []*modeldata.BoundModel                                 { panic("declaration") }
func EchoRows(rows []*modeldata.BoundModel) []*modeldata.BoundModel { panic("declaration") }

func Mutate(row *modeldata.BoundModel) { panic("declaration") }

func AliasRows(part, all []*modeldata.BoundModel) []*modeldata.BoundModel { panic("declaration") }
func AnyRows() any                                                        { panic("declaration") }
func EchoAny(value any) any                                               { panic("declaration") }

func SwapRows(rows []*modeldata.BoundModel)                         { panic("declaration") }
func NilRows() []*modeldata.BoundModel                              { panic("declaration") }
func IsRows(value any) bool                                         { panic("declaration") }
func TailRows(rows []*modeldata.BoundModel) []*modeldata.BoundModel { panic("declaration") }

func WithRows(rows []*modeldata.BoundModel, visit func([]*modeldata.BoundModel) []*modeldata.BoundModel) {
	panic("declaration")
}
func VisitRows(rows []*modeldata.BoundModel, visit func()) { panic("declaration") }

func ProduceRows(produce func() []*modeldata.BoundModel) []*modeldata.BoundModel {
	panic("declaration")
}

func DirectVisit(rows []*modeldata.BoundModel, visit *evm.FunctionOf[func()]) {
	panic("declaration")
}
func PanicRows(rows []*modeldata.BoundModel) { panic("declaration") }

func Matrix() [][]*modeldata.BoundModel                                   { panic("declaration") }
func EchoMatrix(rows [][]*modeldata.BoundModel) [][]*modeldata.BoundModel { panic("declaration") }
func Groups() map[string][]*modeldata.BoundModel                          { panic("declaration") }

type NamedRowsType []*modeldata.BoundModel
type NamedMatrixType [][]*modeldata.BoundModel

func NamedRows() NamedRowsType                                { panic("declaration") }
func NamedEcho(rows NamedRowsType) NamedRowsType              { panic("declaration") }
func NamedIs(value any) bool                                  { panic("declaration") }
func NamedProduce(produce func() NamedRowsType) NamedRowsType { panic("declaration") }
func NamedMatrix() NamedMatrixType                            { panic("declaration") }
func NamedMatrixEcho(rows NamedMatrixType) NamedMatrixType    { panic("declaration") }

func Hidden(r modeldata.BoundModel) int { panic("declaration") }
func WithValue(r modeldata.BoundModel, visit func(modeldata.BoundModel) modeldata.BoundModel) modeldata.BoundModel {
	panic("declaration")
}

func EchoValues(rows []modeldata.BoundModel) []modeldata.BoundModel { panic("declaration") }
