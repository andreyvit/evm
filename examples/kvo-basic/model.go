package main

import (
	"reflect"
	"time"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/values"
)

// This file is the data model: plain Go and KVO, with no knowledge of EVM.

// Customer is the Go struct form of a customer. A kvo tag binds a field to a
// property by name; a msgpack tag names its key in MessagePack rows.
type Customer struct {
	Name     string    `kvo:"name" msgpack:"name"`
	Tags     []string  `kvo:"tags" msgpack:"tags"`
	Earned   int64     `kvo:"points_earned" msgpack:"points_earned"`
	Redeemed int64     `kvo:"points_redeemed" msgpack:"points_redeemed"`
	Joined   time.Time `kvo:"joined" msgpack:"joined"`
}

// Declare each property once, with a stable code and a type. A model lists
// the properties of one record type. Build all of this at startup.
var (
	Schema       = kvo.NewSchema()
	CustomerType = kvo.NewEntityType(Schema, "customer", reflect.TypeFor[Customer]())

	Customer_Name = kvo.NewStringProp[string](Schema, 1, "customer:name", kvo.TMachineString)
	Customer_Tags = kvo.NewListProp(Schema, 2, "customer:tags",
		kvo.ListOf(kvo.TMachineString), values.StringCodec[string]())
	Customer_Earned   = kvo.NewSignedProp[int64](Schema, 3, "customer:points_earned", kvo.TInt64)
	Customer_Redeemed = kvo.NewSignedProp[int64](Schema, 4, "customer:points_redeemed", kvo.TInt64)
	Customer_Joined   = kvo.NewTimeProp(Schema, 5, "customer:joined")

	CustomerModel = kvo.NewModel(Schema, CustomerType, func(b *kvo.ModelBuilder) {
		b.Prop(Customer_Name)
		b.Prop(Customer_Tags)
		b.Prop(Customer_Earned)
		b.Prop(Customer_Redeemed)
		b.Prop(Customer_Joined)
	})

	// Customers makes records of the model from structs and from rows.
	Customers = kvo.Bind(reflect.TypeFor[Customer](), CustomerModel)
)

// PointBalance takes a record in any representation. Scripts call it as a
// method: customer.PointBalance().
func PointBalance(c kvo.Record) int64 {
	return c.Get(Customer_Earned) - c.Get(Customer_Redeemed)
}
