package evm

import (
	"reflect"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

// RegisterNativeCopies exposes NativeCopy on immutable rows with a selected
// native allocation type. Call after choosing allocations and before sharing
// or compiling the package. Encoded rows decode explicitly; native rows use
// ordinary shallow struct copies. No conversion changes the original record.
func RegisterNativeCopies(pkg *Package) {
	for _, row := range pkg.Records {
		if row.Mutable || row.NativeAllocation == nil {
			continue
		}
		typ := row.NativeAllocation
		binding := kvo.Bind(typ, row.Model)
		pkg.Methods = append(pkg.Methods, ExternalMethod{
			Name: "NativeCopy", Receiver: row.Pointer,
			Signature: Signature{Results: []*values.Type{row.Pointer}},
			Call: func(c *Call) error {
				record, err := c.Record(c.Argument(0), row.Model)
				if err != nil {
					return err
				}
				if record.Missing() {
					return c.SetRecordResult(row.Model, kvo.Record{})
				}
				if err := c.ChargeAllocation(uint64(typ.Size())); err != nil {
					return err
				}
				dst := reflect.New(typ)
				if err := binding.TryDecodeRecord(record, dst.Elem()); err != nil {
					return err
				}
				return c.SetRecordResult(row.Model, binding.Record(dst))
			},
		})
	}
}
