package evmbind

import (
	"fmt"
	"reflect"
	"time"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/values"
)

func nativeType(n *node, reg map[string]reflect.Type, cache map[*node]reflect.Type) reflect.Type {
	if t := cache[n]; t != nil {
		return t
	}
	if n.native != nil {
		cache[n] = n.native
		return n.native
	}

	var t reflect.Type
	if n.imported != nil {
		fail("missing native declaration for %s", n.name)
	}
	if n.named {
		t = reg[n.name]
	} else if n.kind == nodeMap {
		t = reflect.MapOf(nativeType(n.key, reg, cache), nativeType(n.elem, reg, cache))
	} else if n.kind == nodeSlice {
		t = reflect.SliceOf(nativeType(n.elem, reg, cache))
	} else if n.kind == nodeNativeFunction {
		args := make([]reflect.Type, len(n.functionArgs))
		outs := make([]reflect.Type, len(n.functionOuts))
		for i, a := range n.functionArgs {
			args[i] = nativeType(a, reg, cache)
		}
		for i, a := range n.functionOuts {
			outs[i] = nativeType(a, reg, cache)
		}
		t = reflect.FuncOf(args, outs, false)
	} else if n.kind == nodePointer {
		t = reflect.PointerTo(nativeType(n.elem, reg, cache))
	} else {
		t = reflectBasics[n.kind]
	}
	if t == nil {
		fail("missing native Go type %s: no bound function, method or field reaches it; add it to Options.Types", n.name)
	}
	cache[n] = t
	switch n.kind {
	case nodeStruct:
		if t.Kind() != reflect.Struct || t.NumField() != len(n.fields)+len(n.omittedFields) {
			fail("native struct mismatch: %s", n.name)
		}
		for _, name := range n.omittedFields {
			if field, ok := t.FieldByName(name); !ok || len(field.Index) != 1 {
				fail("missing omitted native field: %s.%s", n.name, name)
			}
		}
		for i, f := range n.fields {
			rf, found := t.FieldByName(f.name)
			if !found || len(rf.Index) != 1 || rf.Name != f.name || rf.Type != nativeType(f.t, reg, cache) || string(rf.Tag) != n.shape.Fields[i].Tag || rf.Anonymous != n.shape.Fields[i].Embedded {
				fail("native field mismatch: %s.%s", n.name, f.name)
			}
		}
	case nodeInterface:
		if t.Kind() != reflect.Interface {
			fail("native interface mismatch: %s", n.name)
		}
	case nodeMap:
		if t.Kind() != reflect.Map || t.Key() != nativeType(n.key, reg, cache) || t.Elem() != nativeType(n.elem, reg, cache) {
			fail("native map mismatch: %s", n.name)
		}
	case nodeSlice:
		if t.Kind() != reflect.Slice || t.Elem() != nativeType(n.elem, reg, cache) {
			fail("native slice mismatch: %s", n.name)
		}
	case nodeNativeFunction:
		if t.Kind() != reflect.Func || t.IsVariadic() || t.NumIn() != len(n.functionArgs) || t.NumOut() != len(n.functionOuts) {
			fail("native function mismatch: %s", n.name)
		}
		for i, a := range n.functionArgs {
			if t.In(i) != nativeType(a, reg, cache) {
				fail("native function argument mismatch: %s", n.name)
			}
		}
		for i, a := range n.functionOuts {
			if t.Out(i) != nativeType(a, reg, cache) {
				fail("native function result mismatch: %s", n.name)
			}
		}
	case nodePointer, nodeCallback, nodeFunctionHandle, nodeRetainedFunction:
	default:
		if t.Kind() != reflectBasics[n.kind].Kind() {
			fail("native scalar mismatch: %s", n.name)
		}
	}
	return t
}

var reflectBasics = map[nodeKind]reflect.Type{
	nodeAny:              reflect.TypeFor[any](),
	nodeError:            reflect.TypeFor[error](),
	nodeCallback:         reflect.TypeFor[func()](),
	nodeFunctionHandle:   reflect.TypeFor[*evm.NamedFunction](),
	nodeRetainedFunction: reflect.TypeFor[*evm.FunctionHandle](),
	nodeFloat32:          reflect.TypeFor[float32](),
	nodeFloat64:          reflect.TypeFor[float64](),
	nodeString:           reflect.TypeFor[string](),
	nodeBool:             reflect.TypeFor[bool](),
	nodeInt:              reflect.TypeFor[int](),
	nodeInt8:             reflect.TypeFor[int8](),
	nodeInt16:            reflect.TypeFor[int16](),
	nodeInt32:            reflect.TypeFor[int32](),
	nodeInt64:            reflect.TypeFor[int64](),
	nodeUint:             reflect.TypeFor[uint](),
	nodeUint8:            reflect.TypeFor[uint8](),
	nodeUint16:           reflect.TypeFor[uint16](),
	nodeUint32:           reflect.TypeFor[uint32](),
	nodeUint64:           reflect.TypeFor[uint64](),
}

func reflectWrapper(op *operation, host reflect.Type, ts map[*node]reflect.Type, function any) Wrapper {
	var fn reflect.Value
	var signature reflect.Type
	skip := 0
	direct := op.recv == nil && function != nil
	if direct {
		fn = reflect.ValueOf(function)
		signature = fn.Type()
	} else {
		t := host
		if op.recv != nil {
			t = ts[op.recv]
		}
		if t == nil && op.recv == nil {
			fail("function %s has no native implementation: bind it in Options.Functions, or leave it out with Options.Omit", op.name)
		}
		if t == nil {
			fail("missing host for %s", op.key)
		}
		m, ok := t.MethodByName(op.name)
		if !ok && op.recv == nil {
			fail("function %s has no native implementation: bind it in Options.Functions or as a method of %s, or leave it out with Options.Omit", op.name, t)
		}
		if !ok {
			fail("missing native method %s", op.key)
		}
		fn, signature = m.Func, m.Type
		skip = 1
		if op.recv == nil {
			skip = 2
		}
		if t.Kind() == reflect.Interface {
			skip--
		}
	}
	if signature.IsVariadic() != op.variadic || signature.NumIn() != skip+len(op.args) || signature.NumOut() != len(op.outs) {
		fail("native signature mismatch: %s", op.key)
	}
	if op.recv == nil && !direct && signature.In(skip-1) != reflect.TypeFor[*evm.Call]() {
		fail("%s must accept *evm.Call", op.key)
	}
	for i, n := range op.args {
		if signature.In(skip+i) != ts[n] {
			fail("native argument mismatch: %s", op.key)
		}
	}
	for i, n := range op.outs {
		if signature.Out(i) != ts[n] {
			fail("native result mismatch: %s", op.key)
		}
	}
	return func(f *Frame) {
		var args []reflect.Value
		call := fn
		start := 0
		if op.recv == nil {
			if !direct {
				args = append(args, reflect.ValueOf(f.Host), reflect.ValueOf(f.Call))
			}
		} else {
			start = 1
			if ts[op.recv].Kind() == reflect.Interface {
				call = reflect.ValueOf(f.Receiver).MethodByName(op.name)
			} else {
				args = append(args, decode(f, op.recv, f.Argument(0), ts))
			}
		}
		for i, n := range op.args {
			args = append(args, decodeArgument(f, n, f.Argument(start+i), ts))
		}
		var results []reflect.Value
		if op.variadic {
			results = call.CallSlice(args)
		} else {
			results = call.Call(args)
		}
		for i, v := range results {
			f.SetResult(i, encode(f, op.outs[i], v))
		}
	}
}

func decodeArgument(f *Frame, n *node, v values.Value, ts map[*node]reflect.Type) reflect.Value {
	if n.kind == nodeNativeFunction {
		return f.decodeFunction(n, v)
	}
	return decode(f, n, v, ts)
}

func decode(f *Frame, n *node, v values.Value, ts map[*node]reflect.Type) reflect.Value {
	if n.kind == nodeRecord {
		return decodeModel(f, n, v)
	}
	t := ts[n]
	x := reflect.New(t).Elem()
	switch n.kind {
	case nodeTimestamp:
		x.Set(reflect.ValueOf(v.AsTime()))
	case nodeError:
		if value := f.DecodeError(v); value != nil {
			x.Set(reflect.ValueOf(value))
		}
	case nodeAny:
		if value := f.DecodeAny(v); value != nil {
			x.Set(reflect.ValueOf(value))
		}
	case nodeSlice:
		if isModelSlice(n) {
			return f.decodeModelSlice(n, v)
		}
		if v.SliceLen() > v.SliceCap() {
			panic("invalid native slice")
		}
		x.Set(reflect.SliceAt(t.Elem(), NativeSliceData(v, t.Elem()), int(v.SliceCap())).Slice(0, int(v.SliceLen())))
	case nodeMap:
		x.Set(reflect.NewAt(t, unsafe.Pointer(&v.P)).Elem())
	case nodeFunctionHandle:
		if n.boxedFunction {
			return reflect.ValueOf(f.BoxedNamedFunction(v))
		}
		return reflect.ValueOf(f.NamedFunction(v))
	case nodeRetainedFunction:
		if n.boxedFunction {
			return reflect.ValueOf(f.BoxedFunction(v))
		}
		return reflect.ValueOf(f.Function(v))
	case nodeNativeFunction:
		return f.decodeNativeFunction(n, v)
	case nodeCallback:
		return reflect.ValueOf(f.Callback(v))
	case nodeInterface:
		if a := f.DecodeTypedInterface(n.name, v); a != nil {
			x.Set(reflect.ValueOf(a))
		}
	case nodePointer:
		if v.P != nil {
			if v.R == values.NativeAddress {
				return reflect.NewAt(t.Elem(), v.P)
			}
			panic(fmt.Errorf("native pointer %s requires native storage", t))
		}
	case nodeStruct:
		if cell := n.shape.CellStruct; cell != nil {
			for i, field := range n.fields {
				x.FieldByName(field.name).Set(decode(f, field.t, cell.Get(v, i), ts))
			}
			return x
		}
		if len(n.omittedFields) > 0 && v.P != nil {
			if snapshot := (*values.Value)(values.CellAt(v.P, n.shape.Cells-1)).P; snapshot != nil {
				x.Set(reflect.NewAt(t, snapshot).Elem())
			}
		}
		for _, field := range n.fields {
			x.FieldByName(field.name).Set(decode(f, field.t, field.t.shape.Stored(values.CellAt(v.P, field.offset)), ts))
		}
	case nodeFloat32:
		x.SetFloat(float64(v.AsFloat32()))
	case nodeFloat64:
		x.SetFloat(v.AsFloat())
	case nodeString:
		x.SetString(v.AsString())
	case nodeBool:
		x.SetBool(v.R != 0)
	default:
		if n.shape.Kind == values.IntKind {
			x.SetInt(int64(v.R))
		} else {
			x.SetUint(v.R)
		}
	}
	return x
}
func store(f *Frame, n *node, p unsafe.Pointer, x reflect.Value) {
	if n.kind != nodeStruct || n.shape.CellStruct != nil {
		n.shape.Store(p, encode(f, n, x))
		return
	}
	if len(n.omittedFields) > 0 {
		if err := f.ChargeAllocation(uint64(x.Type().Size())); err != nil {
			panic(err)
		}
		snapshot := reflect.New(x.Type())
		snapshot.Elem().Set(x)
		*(*values.Value)(values.CellAt(p, n.shape.Cells-1)) = values.Value{P: snapshot.UnsafePointer()}
	}
	for _, field := range n.fields {
		field.t.shape.Store(values.CellAt(p, field.offset), encode(f, field.t, x.FieldByName(field.name)))
	}
}
func encode(f *Frame, n *node, x reflect.Value) values.Value {
	if n.kind == nodeRecord {
		return encodeModel(f, n, x)
	}
	switch n.kind {
	case nodeTimestamp:
		return f.Time(x.Interface().(time.Time))
	case nodeAny, nodeError:
		return f.EncodeAny(x.Interface())
	case nodeSlice:
		if isModelSlice(n) {
			return f.encodeModelSlice(n, x)
		}
		if uint64(x.Cap()) > 1<<32-1 {
			panic("native slice exceeds VM capacity")
		}
		return values.SliceValue(x.UnsafePointer(), uint32(x.Len()), uint32(x.Cap()))
	case nodeMap:
		return values.Value{P: x.UnsafePointer()}
	case nodeNativeFunction:
		return f.encodeNativeFunction(n, x)
	case nodeRetainedFunction:
		return f.EncodeFunction(n.name, x.Interface().(*evm.FunctionHandle))
	case nodeInterface:
		return f.EncodeTypedInterface(n.name, x.Interface())
	case nodePointer:
		if x.IsNil() {
			return values.Value{}
		}

		return values.Value{R: values.NativeAddress, P: x.UnsafePointer()}
	case nodeStruct:
		if cell := n.shape.CellStruct; cell != nil {
			var fields [2]values.Value
			for i, field := range n.fields {
				fields[i] = encode(f, field.t, x.FieldByName(field.name))
			}
			return cell.Pack(fields)
		}
		p := f.New(n.shape.Cells)
		store(f, n, p, x)
		return values.Value{P: p}
	case nodeFloat32:
		return values.Float32(float32(x.Float()))
	case nodeFloat64:
		return values.Float(x.Float())
	case nodeString:
		return f.String(x.String())
	case nodeBool:
		return values.Bool(x.Bool())
	default:
		if n.shape.Kind == values.IntKind {
			return values.Int(x.Int())
		}
		if n.shape.Kind == values.UintKind {
			return values.Uint(x.Uint())
		}
		panic(fmt.Errorf("unsupported %s", n.name))
	}
}

// Infer unnamed ABI shapes before the consumer's native types are resolved.
// Shared VM model identities must never merge distinct native containers.
func inferredNativeType(n *node) reflect.Type {
	if n.native != nil {
		return n.native
	}
	if n.named {
		return nil
	}
	switch n.kind {
	case nodePointer, nodeSlice:
		elem := inferredNativeType(n.elem)
		if elem == nil {
			return nil
		}
		if n.kind == nodePointer {
			return reflect.PointerTo(elem)
		}
		return reflect.SliceOf(elem)
	case nodeMap:
		key, elem := inferredNativeType(n.key), inferredNativeType(n.elem)
		if key == nil || elem == nil {
			return nil
		}
		return reflect.MapOf(key, elem)
	default:
		return reflectBasics[n.kind]
	}
}
