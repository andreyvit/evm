package contentfixture

import "github.com/andreyvit/evm/gox"

type Envelope struct{ Title gox.Content }

func Echo(x gox.Content) gox.Content { return x }
func Wrap(x gox.Content) Envelope    { return Envelope{x} }
