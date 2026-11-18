package modeldata

import (
	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/kvo"
	"reflect"
	"testing/fstest"
	"time"
)

func Declarations() (evm.Package, *evmbind.Schema, error) {
	schema := kvo.NewSchema()
	prop := kvo.NewStringProp[string](schema, 1, "name", kvo.TMachineString)
	kvo.NewModel(schema, kvo.NewEntityType(schema, "Customer", reflect.TypeFor[BoundModel](), reflect.TypeFor[Alternate]()), func(b *kvo.ModelBuilder) { b.Prop(prop) })
	kvo.NewModel(schema, kvo.NewEntityType(schema, "GenericString", reflect.TypeFor[Generic[string]]()), func(b *kvo.ModelBuilder) { b.Prop(prop) })
	kvo.NewModel(schema, kvo.NewEntityType(schema, "GenericCustomer", reflect.TypeFor[Generic[BoundModel]]()), func(b *kvo.ModelBuilder) { b.Prop(prop) })
	kvo.NewModel(schema, kvo.NewEntityType(schema, "GenericDuration", reflect.TypeFor[Generic[time.Duration]]()), func(b *kvo.ModelBuilder) { b.Prop(prop) })
	models, err := evm.ModelPackage("db", schema)
	if err != nil {
		return models, nil, err
	}
	// The fixture explicitly chooses one native allocation layout per model.
	for _, row := range models.Records {
		if row.Mutable {
			continue
		}
		switch row.Name {
		case "Customer":
			row.NativeAllocation = reflect.TypeFor[BoundModel]()
		case "GenericString":
			row.NativeAllocation = reflect.TypeFor[Generic[string]]()
		case "GenericCustomer":
			row.NativeAllocation = reflect.TypeFor[Generic[BoundModel]]()
		case "GenericDuration":
			row.NativeAllocation = reflect.TypeFor[Generic[time.Duration]]()
		}
	}
	duration, err := evmbind.New("time", fstest.MapFS{"time.go": {Data: []byte("package time; type Duration int64")}}, evmbind.Options{Selective: true, Types: []reflect.Type{reflect.TypeFor[time.Duration]()}})
	if err != nil {
		return models, nil, err
	}
	dep, err := evmbind.New("model", Source, evmbind.Options{Selective: true, Models: &models, NativePackage: reflect.TypeFor[BoundModel]().PkgPath(), Imports: map[string]*evmbind.Schema{"time": duration}})
	return models, dep, err
}
