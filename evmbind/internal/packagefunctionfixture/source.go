package packagefunctionfixture

import (
	"embed"
	"github.com/andreyvit/evm/evmbind"
)

//go:embed api.go
var Source embed.FS

func Options() evmbind.Options {
	return evmbind.Options{Methods: map[string][]string{"Counter": {"Add"}}, Functions: map[string]any{
		"NewGroup": NewGroup, "Sum": Sum, "SetFirst": SetFirst, "AsAdder": AsAdder, "Reset": Reset, "RangeOf": RangeOf, "NewCounter": NewCounter, "Increment": Increment, "Double": Double, "Visit": Visit, "Split": Split,
	}}
}
