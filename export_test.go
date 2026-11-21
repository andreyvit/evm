package evm

import (
	"encoding/binary"
	"hash/maphash"
	"math"
	"math/bits"
	"reflect"
	"unsafe"

	"github.com/andreyvit/evm/values"
)

var ProgramListing = programListing
var ModuleProgramListing = moduleProgramListing

// SharedShapes reports how many linked shapes a program takes from its
// environment and how many it snapshotted for itself.
func SharedShapes(p *Program) (shared, own int) {
	for _, word := range p.sharedSeen {
		shared += bits.OnesCount64(word)
	}
	return shared, len(p.shapeByType)
}

// SharedMethods reports how many of a program's method tables are the
// environment's and how many the program built, and whether the natives
// those tables name are the environment's.
func (e *LinkEnvironment) SharedMethods(p *Program) (shared, own int, natives bool) {
	tables := make(map[unsafe.Pointer]bool)
	for _, s := range e.methods {
		for _, table := range s.types {
			if table.linked != nil {
				tables[reflect.ValueOf(table.linked).UnsafePointer()] = true
			}
		}
	}
	for _, t := range p.types {
		if t.methods == nil {
			continue
		}
		if tables[reflect.ValueOf(t.methods).UnsafePointer()] {
			shared++
		} else {
			own++
		}
	}
	return shared, own, len(p.methodNatives) != 0 && len(e.natives) != 0 && &p.methodNatives[0] == &e.natives[0]
}

// MethodNatives returns how many natives a program has for its bytecode and
// how many of its own for its method tables.
func MethodNatives(p *Program, e *LinkEnvironment) (static, methods int) {
	if len(p.methodNatives) != 0 && len(e.natives) != 0 && &p.methodNatives[0] == &e.natives[0] {
		return p.nativeImports, 0
	}
	return p.nativeImports, len(p.methodNatives)
}

// Fingerprint hashes everything programs share through the environment:
// shape snapshots with their native layouts, resolved bindings, method
// tables, method IDs, codecs and native slice layouts. It changes if
// anything writes to them.
func (e *LinkEnvironment) Fingerprint() uint64 {
	h := fingerprint{visited: make(map[unsafe.Pointer]bool)}
	h.hash.SetSeed(fingerprintSeed)
	var total uint64
	add := func(v reflect.Value) {
		// Each item is hashed alone: maps are visited in any order, and an
		// item must hash what it shares with another either way.
		h.hash.Reset()
		clear(h.visited)
		h.walk(v)
		total += h.hash.Sum64()
	}
	if e.shapes != nil {
		for _, s := range e.shapes.snapshots {
			add(reflect.ValueOf(s).Elem())
		}
		add(reflect.ValueOf(e.shapes.children))
		add(reflect.ValueOf(e.shapes.sameType))
	}
	for _, x := range e.exports {
		if g := x.resolved; g != nil {
			add(reflect.ValueOf(&g.ExternalFunction).Elem())
			add(reflect.ValueOf(&g.params).Elem())
			add(reflect.ValueOf(&g.results).Elem())
			add(reflect.ValueOf(g.form))
			add(reflect.ValueOf(g.record))
			add(reflect.ValueOf(&g.resultString).Elem())
			add(reflect.ValueOf(&g.rSize).Elem())
			add(reflect.ValueOf(&g.pSize).Elem())
		}
	}
	// The method tables programs share, with the IDs their keys come from.
	for name, id := range e.methodIDs {
		add(reflect.ValueOf(&name).Elem())
		h.word(uint64(id))
		total += h.hash.Sum64()
	}
	for _, s := range e.methods {
		add(reflect.ValueOf(&s.native).Elem())
		add(reflect.ValueOf(&s.imports).Elem())
		for _, table := range s.types {
			add(reflect.ValueOf(&table.methods).Elem())
			for id, method := range table.linked {
				add(reflect.ValueOf(method).Elem())
				h.word(uint64(id))
				total += h.hash.Sum64()
			}
		}
	}
	for _, codec := range e.nativeCodecs {
		add(reflect.ValueOf(codec))
	}
	for _, b := range e.nativeSlices {
		add(reflect.ValueOf(b.decl))
	}
	return total
}

var fingerprintSeed = maphash.MakeSeed()

// fingerprintFollowed are the types hashed through pointers. Pointers to
// other types are hashed by address; each shape snapshot is hashed itself.
var fingerprintFollowed = map[reflect.Type]bool{
	reflect.TypeFor[values.NativeLayout]():       true,
	reflect.TypeFor[values.NativeSliceLayout]():  true,
	reflect.TypeFor[values.NativeMapLayout]():    true,
	reflect.TypeFor[values.NativeCompare]():      true,
	reflect.TypeFor[values.NativeAddressCodec](): true,
	reflect.TypeFor[values.NativeValueCodec]():   true,
	reflect.TypeFor[NativeValueCodec]():          true,
	reflect.TypeFor[NativeSliceDecl]():           true,
	reflect.TypeFor[OwnershipEffects]():          true,
	reflect.TypeFor[hostForm]():                  true,
	reflect.TypeFor[RecordType]():                true,
}

type fingerprint struct {
	hash    maphash.Hash
	visited map[unsafe.Pointer]bool
}

func (h *fingerprint) word(x uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], x)
	h.hash.Write(b[:])
}

func (h *fingerprint) walk(v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		p := v.UnsafePointer()
		h.word(uint64(uintptr(p)))
		if p != nil && fingerprintFollowed[v.Type().Elem()] && !h.visited[p] {
			h.visited[p] = true
			h.walk(v.Elem())
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Field(i)
			if !f.CanInterface() && f.CanAddr() {
				f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
			}
			h.walk(f)
		}
	case reflect.Slice:
		h.word(uint64(uintptr(v.UnsafePointer())))
		h.word(uint64(v.Len()))
		for i := 0; i < v.Len(); i++ {
			h.walk(v.Index(i))
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			h.walk(v.Index(i))
		}
	case reflect.String:
		h.word(uint64(v.Len()))
		h.hash.WriteString(v.String())
	case reflect.Func, reflect.Map, reflect.Chan, reflect.UnsafePointer, reflect.Interface:
		// The words of the value: a closure, a map or an interface is the
		// same one as long as they do not change.
		if !v.CanAddr() {
			panic("fingerprint: value is not addressable")
		}
		words := unsafe.Slice((*uintptr)(unsafe.Pointer(v.UnsafeAddr())), v.Type().Size()/unsafe.Sizeof(uintptr(0)))
		for _, w := range words {
			h.word(uint64(w))
		}
	case reflect.Bool:
		if v.Bool() {
			h.word(1)
		} else {
			h.word(0)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		h.word(uint64(v.Int()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		h.word(v.Uint())
	case reflect.Float32, reflect.Float64:
		h.word(math.Float64bits(v.Float()))
	default:
		panic("fingerprint: unhandled kind " + v.Kind().String())
	}
}
