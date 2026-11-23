// Package cachecore binds the guest cache to its host-owned storage.
package cachecore

import (
	"embed"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/evmruntime/concurrency"
	"github.com/andreyvit/evm/values"
	"io/fs"
	"reflect"
	"strings"
	"sync"
	"time"
	"unsafe"
)

//go:embed _decl/decl.go
var source embed.FS

type Store struct {
	cache *concurrency.Cache[cacheKey, values.Value]
	owner *evm.Program
}
type Ticket struct {
	entry concurrency.Entry[values.Value]
}
type cacheKey struct {
	word  uint64
	shape *values.Shape
	data  string
}

// Bound the work even for arrays with billions of zero-sized elements.
const maxCacheParts = 4096
const maxCacheBytes = 1 << 20

type cacheData struct {
	vm           *evm.VM
	parts, bytes int
	key          []byte
}

// For a key, write an unambiguous encoding. For a value, copy immutable leaves
// into new guest storage. A cache entry never retains the caller's backing array.
func (d *cacheData) visit(s *values.Shape, v values.Value, dst unsafe.Pointer, key bool) {
	d.parts++
	if d.parts > maxCacheParts {
		panic("concurrency: cache item has too many fields")
	}
	switch s.Kind {
	case values.BoolKind, values.IntKind, values.UintKind, values.FloatKind:
		if key {
			if s.Kind == values.FloatKind {
				panic("concurrency: float cache keys are unsupported")
			}
			d.key = binary.LittleEndian.AppendUint64(d.key, v.R)
		} else {
			s.Store(dst, v)
		}
	case values.StringKind:
		str := v.AsString()
		d.reserve(len(str))
		if key {
			d.key = binary.AppendUvarint(d.key, uint64(len(str)))
			d.key = append(d.key, str...)
		} else {
			s.Store(dst, values.String(strings.Clone(str)))
		}
	case values.StructKind, values.ArrayKind:
		if s.Native != nil && s.Native.OpaqueCell != 0 || s.CellStruct != nil || v.R != 0 {
			panic(fmt.Sprintf("concurrency: unsupported cache aggregate %s", s.Type.Name()))
		}
		child := func(f *values.Shape, offset uint64) {
			var out unsafe.Pointer
			if !key {
				out = values.CellAt(dst, offset)
			}
			d.visit(f, f.Stored(values.CellAt(v.P, offset)), out, key)
		}
		if s.Kind == values.StructKind {
			for _, f := range s.Fields {
				if f.Name != "_" {
					child(f.Shape, f.Offset)
				}
			}
		} else {
			for i := uint64(0); i < s.Len; i++ {
				child(s.Elem, i*s.Elem.Cells)
			}
		}
	default:
		panic(fmt.Sprintf("concurrency: unsupported cache data type %s", s.Type.Name()))
	}
}
func (d *cacheData) reserve(n int) {
	if n > maxCacheBytes-d.bytes {
		panic("concurrency: cache item exceeds 1 MiB")
	}
	d.bytes += n
	if n != 0 {
		if err := d.vm.ChargeAllocation(uint64(n)); err != nil {
			panic(err)
		}
	}
}

func keyOf(vm *evm.VM, packed values.Value, retain bool) cacheKey {
	s, v, err := vm.Program().UnpackInterface(packed)
	if err != nil {
		panic(err)
	}
	if s == nil {
		panic("concurrency: nil cache key")
	}
	k := cacheKey{shape: s}
	switch s.Kind {
	case values.IntKind, values.UintKind, values.BoolKind:
		k.word = v.R
	case values.StringKind:
		k.data = v.AsString()
		if len(k.data) > maxCacheBytes {
			panic("concurrency: cache item exceeds 1 MiB")
		}
		if retain {
			d := cacheData{vm: vm}
			d.reserve(len(k.data))
			k.data = strings.Clone(k.data)
		}
	default:
		d := cacheData{vm: vm}
		d.visit(s, v, nil, true)
		if err := vm.ChargeAllocation(uint64(len(d.key))); err != nil {
			panic(err)
		}
		k.data = string(d.key)
	}
	return k
}

func snapshot(vm *evm.VM, packed values.Value) values.Value {
	s, v, err := vm.Program().UnpackInterface(packed)
	if err != nil {
		panic(err)
	}
	if s == nil {
		panic("concurrency: nil cache value")
	}
	d := cacheData{vm: vm}
	switch s.Kind {
	case values.IntKind, values.UintKind, values.BoolKind, values.FloatKind:
		return packed
	case values.StringKind:
		str := v.AsString()
		d.reserve(len(str))
		v = values.String(strings.Clone(str))
	case values.StructKind, values.ArrayKind:
		if s.Cells > maxCacheParts {
			panic("concurrency: cache item has too many fields")
		}
		ptr, err := vm.AllocCells(s.Cells)
		if err != nil {
			panic(err)
		}
		d.visit(s, v, ptr, false)
		v = s.Stored(ptr)
	default:
		panic(fmt.Sprintf("concurrency: unsupported cache value type %s", s.Type.Name()))
	}
	if err := vm.ChargeAllocation(uint64(unsafe.Sizeof(values.Value{}))); err != nil {
		panic(err)
	}
	return must(vm.Program().PackInterface(s.Type, v))
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

type binding struct {
	stores  *evmbind.ValueCodec[Store]
	tickets *evmbind.ValueCodec[Ticket]
}

func (b binding) store(vm *evm.VM) *concurrency.Cache[cacheKey, values.Value] {
	s := must(b.stores.UnpackIn(vm, vm.CallArgument(0)))
	if s.cache == nil || s.owner != vm.Program() {
		panic("concurrency: use NewCache in the current program")
	}
	return s.cache
}
func (b binding) newCache(vm *evm.VM) error {
	s := Store{concurrency.NewCache[cacheKey, values.Value](), vm.Program()}
	vm.SetCallResult(0, must(b.stores.PackIn(vm, s)))
	return nil
}
func (b binding) setTicket(vm *evm.VM, i int, e concurrency.Entry[values.Value]) {
	vm.SetCallResult(i, must(b.tickets.PackIn(vm, Ticket{e})))
}
func (b binding) get(vm *evm.VM) error {
	e, ok := b.store(vm).Get(keyOf(vm, vm.CallArgument(1), false))
	vm.SetCallResult(0, e.Value)
	b.setTicket(vm, 1, e)
	vm.SetCallResult(2, values.Bool(ok))
	return nil
}
func (b binding) set(vm *evm.VM) error {
	k := keyOf(vm, vm.CallArgument(1), true)
	v := snapshot(vm, vm.CallArgument(2))
	e := b.store(vm).Set(k, v, vm.CallArgument(3).AsTime())
	b.setTicket(vm, 0, e)
	return nil
}
func (b binding) invalidate(vm *evm.VM) error {
	e := must(b.tickets.UnpackIn(vm, vm.CallArgument(2)))
	b.store(vm).InvalidateIfCurrent(keyOf(vm, vm.CallArgument(1), false), e.entry)
	return nil
}

// A nested miss could wait for its own loader, including across caches.
// Entries exist only while the synchronous guest callback runs.
var activeLoaders sync.Map
var loaderSignature = evm.Signature{Results: []*values.Type{values.AnyType, values.TimeType, values.ErrorType}}
var loaderFailed = errors.New("cache loader returned an error")

func (b binding) load(vm *evm.VM) error {
	c := b.store(vm)
	key := keyOf(vm, vm.CallArgument(1), false)
	loader := vm.CallArgument(2)
	entry, hit := c.Get(key)
	if !hit {
		vm.SetCallResult(0, values.Value{})
		vm.SetCallResult(1, values.Value{})
		if _, active := activeLoaders.Load(vm); active {
			return vm.SetError(2, errors.New("concurrency: nested cache load would block"))
		}
		// A miss can keep a string key after the invocation. Hits only borrow it.
		if key.shape.Kind == values.StringKind {
			d := cacheData{vm: vm}
			d.reserve(len(key.data))
			key.data = strings.Clone(key.data)
		}
		s, raw, err := vm.Program().UnpackInterface(loader)
		if err != nil {
			return err
		}
		if s == nil || s.Kind != values.FunctionKind {
			return errors.New("concurrency: expected cache loader function")
		}
		fn, err := vm.Program().Callable(raw)
		if err != nil {
			return err
		}
		if err := fn.CheckSignature(loaderSignature); err != nil {
			return err
		}
		var results [3]values.Value
		var trap error
		entry, err = c.GetOrLoad(vm.Context, key, func() (values.Value, time.Time, error) {
			activeLoaders.Store(vm, true)
			defer activeLoaders.Delete(vm)
			trap = vm.Run(fn, results[:])
			if trap != nil {
				return values.Value{}, time.Time{}, trap
			}
			if results[2] != (values.Value{}) {
				return values.Value{}, time.Time{}, loaderFailed
			}
			return snapshot(vm, results[0]), results[1].AsTime(), nil
		})
		if trap != nil {
			return vm.CallbackError(trap)
		}
		if err == loaderFailed {
			vm.SetCallResult(2, results[2])
			return nil
		}
		if err != nil {
			return vm.SetError(2, err)
		}
	}
	vm.SetCallResult(0, entry.Value)
	b.setTicket(vm, 1, entry)
	vm.SetCallResult(2, values.Value{})
	return nil
}

// Package exposes only value-copy handles. Guest code cannot reach the native
// mutex, map, or entry identity through a field or pointer.
func Package() evm.Package {
	files := must(fs.Sub(source, "_decl"))
	schema := must(evmbind.New("evmruntime/concurrency/internal/cachecore", files, evmbind.Options{Types: []reflect.Type{reflect.TypeFor[Store](), reflect.TypeFor[Ticket]()}}))
	b := binding{evmbind.Codec[Store](schema), evmbind.Codec[Ticket](schema)}
	pkg := schema.Bind(nil)
	add := func(name string, in, out []*values.Type, run evm.Native) {
		pkg.Functions = append(pkg.Functions, evm.ExternalFunction{Name: name, Signature: evm.Signature{Params: in, Results: out}, Call: func(vm *evm.VM) (err error) { defer evmbind.Catch(name, &err); return run(vm) }})
	}
	store, ticket := b.stores.Type(), b.tickets.Type()
	anyType := values.AnyType
	add("New", nil, []*values.Type{store}, b.newCache)
	add("Get", []*values.Type{store, anyType}, []*values.Type{anyType, ticket, values.BoolType}, b.get)
	add("Set", []*values.Type{store, anyType, anyType, values.TimeType}, []*values.Type{ticket}, b.set)
	add("Invalidate", []*values.Type{store, anyType, ticket}, nil, b.invalidate)
	add("Load", []*values.Type{store, anyType, anyType}, []*values.Type{anyType, ticket, values.ErrorType}, b.load)
	return pkg
}
