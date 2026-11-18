package modeldata

type BoundModel modelFields

type modelFields struct {
	Name   string `kvo:"name"`
	Hidden int    `kvo:"-"`
}

type Alternate struct {
	Label string `kvo:"name"`
}

type Constraint interface{ ~string | ~int64 | BoundModel }

type Generic[T Constraint] struct {
	Name   string `kvo:"name"`
	Hidden T      `kvo:"-"`
}
