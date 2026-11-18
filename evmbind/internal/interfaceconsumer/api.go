package interfaceconsumer

import "github.com/andreyvit/evm/evmbind/internal/interfacedep"

func Get() interfacedep.Named                      { panic("host") }
func Echo(x interfacedep.Named) interfacedep.Named { panic("host") }

type LocalNamed interface{ Label() string }

func EchoLocal(x LocalNamed) LocalNamed { panic("host") }

func Any() any { panic("host") }
