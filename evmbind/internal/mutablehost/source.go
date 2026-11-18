package mutablehost

import (
	"embed"
	"github.com/andreyvit/evm/evmbind"
)

//go:embed api.go
var Source embed.FS

func Annotations() map[string]evmbind.MutableSlots {
	return map[string]evmbind.MutableSlots{
		"RowService.Replace": {Params: []int{0}, Results: []int{0}},
		"Service.Replace":    {Params: []int{0}, Results: []int{0}},
		"Get":                {Results: []int{0}},
		"Echo":               {Params: []int{0}, Results: []int{0}},
		"Other":              {Results: []int{0}},
	}
}
