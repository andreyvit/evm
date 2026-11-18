package aliasconsumer

import "github.com/andreyvit/evm/evmbind/internal/importfixture"

type Child = importfixture.Child
type Permission = importfixture.Permission

func Echo(child *Child) *Child { panic("declaration") }

func Many(children []*Child) map[string]*Child { panic("declaration") }

const Read = importfixture.Read
const Write = importfixture.Write
