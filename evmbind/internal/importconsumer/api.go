package importconsumer

import "github.com/andreyvit/evm/evmbind/internal/importfixture"

type Options struct {
	Boxed      importfixture.Box[importfixture.Permission]
	Permission importfixture.Permission
	Child      *importfixture.Child
}

func Echo(options *Options) importfixture.Permission { panic("host") }
