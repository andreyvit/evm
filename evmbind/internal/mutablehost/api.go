package mutablehost

import "github.com/andreyvit/evm/evmbind/internal/modeldata"

func Get() *modeldata.BoundModel                           { panic("declaration") }
func Echo(row *modeldata.BoundModel) *modeldata.BoundModel { panic("declaration") }
func Other() *modeldata.Alternate                          { panic("declaration") }
func Ordinary() *modeldata.BoundModel                      { panic("declaration") }
func EchoAny(value any) any                                { panic("declaration") }

type Service struct{}

func (*Service) Replace(row *modeldata.BoundModel) *modeldata.BoundModel { return row }
func ServiceInstance() *Service                                          { panic("declaration") }

func CheckSlices(tags []string, data []byte, numbers []int64) bool { panic("declaration") }

type RowService interface {
	Replace(*modeldata.BoundModel) *modeldata.BoundModel
}

func InterfaceService() RowService { panic("declaration") }

type SliceSink struct {
	Tags    []string
	Bytes   []byte
	Numbers []int64
	Dict    map[string][]string
}

func Sink() *SliceSink { panic("declaration") }
