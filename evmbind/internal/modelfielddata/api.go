package modelfielddata

import "github.com/andreyvit/evm/evmbind/internal/modeldata"

type Context struct {
	Value    modeldata.BoundModel
	Customer *modeldata.BoundModel
	Other    *modeldata.BoundModel
}

func (c *Context) Clone() Context { return *c }
