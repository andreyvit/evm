package evm

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"math/bits"
	"path"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/andreyvit/evm/kvo"
	"github.com/andreyvit/evm/kvo/codec/row2"
	"github.com/andreyvit/evm/values"
)

type Signature struct{ Params, Results []*values.Type }

// Layout allocates V pairs first, then independent R and P slots. Slots retains
// source order, so a wrapper can derive its ABI once during registration.
type Layout struct {
	Slots          []uint16
	RCount, PCount int
}

func LayoutOf(types []*values.Type) (Layout, error) {
	if len(types) > 65535 {
		return Layout{}, fmt.Errorf("too many values in signature")
	}
	l := Layout{Slots: make([]uint16, len(types))}
	for i, t := range types {
		if t == nil || t.Repr() < values.Raw || t.Repr() > values.Pair {
			return l, fmt.Errorf("invalid type in signature")
		}
		if t.Repr() == values.Pair {
			l.Slots[i] = uint16(l.RCount)
			l.RCount++
			l.PCount++
		}
	}
	for i, t := range types {
		switch t.Repr() {
		case values.Raw:
			l.Slots[i] = uint16(l.RCount)
			l.RCount++
		case values.Pointer:
			l.Slots[i] = uint16(l.PCount)
			l.PCount++
		}
	}
	return l, nil
}

// ParameterLayout reserves incoming V0 for a method's receiver. The explicit
// arguments use the ordinary packed layout starting at R1 and P1.
func ParameterLayout(params []*values.Type, method bool) (Layout, error) {
	if !method {
		return LayoutOf(params)
	}
	if len(params) == 0 || len(params) > 65535 || params[0] == nil || params[0].Repr() < values.Raw || params[0].Repr() > values.Pair {
		return Layout{}, fmt.Errorf("invalid method parameters")
	}
	args, err := LayoutOf(params[1:])
	if err != nil {
		return Layout{}, err
	}
	slots := make([]uint16, len(params))
	for i, slot := range args.Slots {
		slots[i+1] = slot + 1
	}
	return Layout{Slots: slots, RCount: args.RCount + 1, PCount: args.PCount + 1}, nil
}

// SourceBranch associates duplicated machine tests with one source condition.
// PC identifies its canonical instruction; Inverted reverses jump outcomes.
type SourceBranch struct {
	PC       int
	Inverted bool
}

type Function struct {
	// OwnershipSites describes compiler-emitted clone locations before linking.
	OwnershipSites []OwnershipSite
	// LocalRecordReads marks instructions whose result cannot outlive its next execution in this frame.
	LocalRecordReads map[int]bool
	Method           bool // The first parameter is the receiver.
	// RecoverPC is a bytecode PC plus one; zero means no recovery continuation.
	RecoverPC uint32
	// RecoverTransparent marks compiler-generated receiver forwarding wrappers.
	RecoverTransparent bool
	Captures           []CaptureSlot
	Internal           bool // Generated functions are callable by bytecode only.
	Name               string
	Position           SourcePosition
	Signature          Signature
	// RCount/PCount cover this function's parameters, results and locals.
	// Outgoing arguments/results follow them and are sized by the linker.
	RCount, PCount int
	Code           []Instruction
	// Lines is optional; when present it has one source line per instruction.
	Lines []int
	// Positions is optional too, and takes precedence over Lines. Without
	// either, every instruction has the zero position.
	Positions []SourcePosition
}
type Import struct {
	TypeArgs      []*values.Type
	Ownership     *OwnershipEffects
	Receiver      *values.Type
	Package, Name string
	Signature     Signature
}

// JSONTypes identifies the concrete types a KVO JSON value can produce.
type JSONTypes struct{ Boolean, Integer, Unsigned, Float, String, Array, Object uint32 }

type Module struct {
	JSONTypes JSONTypes
	// NoOptimization disables optional linker rewrites. The compiler sets this for debug modules.
	NoOptimization bool
	// StrictCallWindows promises that callers only observe declared results after a script call.
	StrictCallWindows bool
	// Sources holds original text keyed by source-position filename for optional disassembly comments.
	Sources      map[string]string
	Defers       []DeferOp
	Types        []DynamicType // concrete interface types indexed by R; slot zero, if present, must be empty
	Invokes      []Invoke
	FunctionRefs []FunctionRef
	Closures     []Closure
	Signatures   []Signature
	Functions    []Function
	// Imports lists the natives that instructions, function references and
	// defers name by index, and any native a method table names here.
	Imports []Import
	// MethodImports lists native methods for the method tables of Types
	// alone; see FunctionRef. A link binds those the tables name, so the
	// list may hold more. Modules compiled through one environment share
	// one list and the method tables of host types that name nothing else:
	// the module does not own them and they must not change.
	MethodImports []Import
	Words         []uint64
	Strings       []string
	Conversions   []Conversion
	// Shapes lists the shapes instructions index, then others the program
	// must know by type.
	Shapes []*values.Shape
	// HostShapes lists more shapes for the program to know by type.
	HostShapes  HostShapes
	ExtendedOps []ExtendedOp
	Globals     []*values.Shape
	Init        string
	// SourceBranches is reporting metadata keyed by function name, then instruction PC.
	SourceBranches map[string]map[int]SourceBranch
}

// HostShapes selects shapes from a table that the modules of one compile
// environment share: the shapes of the host types a module's source can
// reach, a few thousand on a large host. The module does not own the table
// and must not change it.
type HostShapes struct {
	Table []*values.Shape
	// Set has bit i%64 of word i/64 set for each Table[i] the module lists.
	Set []uint64
	// After is the number of leading Module.Shapes that the selection
	// follows in the order of the listing; see Module.ListedShapes.
	After int
}

// ListedShapes yields every shape the module lists, in the order a link
// takes them: a type with several shapes resolves to the first. The
// selection of HostShapes follows the first HostShapes.After of Shapes.
func (m *Module) ListedShapes() iter.Seq[*values.Shape] {
	return func(yield func(*values.Shape) bool) {
		after := min(max(m.HostShapes.After, 0), len(m.Shapes))
		for _, s := range m.Shapes[:after] {
			if !yield(s) {
				return
			}
		}
		for i, word := range m.HostShapes.Set {
			for ; word != 0; word &= word - 1 {
				n := i*64 + bits.TrailingZeros64(word)
				if n >= len(m.HostShapes.Table) {
					return // Link rejects the module.
				}
				if !yield(m.HostShapes.Table[n]) {
					return
				}
			}
		}
		for _, s := range m.Shapes[after:] {
			if !yield(s) {
				return
			}
		}
	}
}

// Call borrows an in-place argument/result window for this native call. Read
// parameters before overwriting result slots. Do not retain Call or its slices,
// and do not reenter this VM. Independent VMs may run concurrently.
type Call struct {
	vm           *VM
	usedR, usedP int // live parent register span when entering a synchronous callback
	program      *Program
	output       func(string) error
	native       *linkedNative
	budget       *executionBudget
	Context      context.Context
	Position     SourcePosition
	R            []uint64
	P            []unsafe.Pointer
}

// Value reads a V pair. Access R-only and P-only arguments through their banks.
func (c *Call) Value(slot int) values.Value { return values.Value{R: c.R[slot], P: c.P[slot]} }

// SetValue writes a V pair into a result slot whose external type uses both banks.
func (c *Call) SetValue(slot int, v values.Value) { c.R[slot], c.P[slot] = v.R, v.P }

type Native func(*Call) error

// LinkName identifies the function in imports and disassembly.
func (f *ExternalFunction) LinkName() string {
	if f.On != nil {
		return f.On.Name + "." + f.Name
	}
	return f.Name
}

type ExternalFunction struct {
	// Instantiate binds a generic source declaration for concrete type arguments.
	// The returned binding must have a concrete signature and no Instantiate hook.
	Instantiate func([]*values.Type) (ExternalFunction, error)
	Ownership   *OwnershipEffects
	// ParamNames preserves declaration names for source frontends such as GoX.
	ParamNames []string
	// Doc preserves declaration comments for source frontends.
	Doc    string
	Direct DirectBinding
	// On exposes a receiver-free operation as pkg.Record.Operation(args).
	On       *RecordType
	panics   bool
	Variadic bool
	// DynamicTypes declares concrete interface results produced by this wrapper.
	DynamicTypes []*values.Type
	Name         string
	Signature    Signature
	Call         Native
}

// SharedType maps a host scalar to a type declared in package sources.
// Its methods are compiled from that same source.
type SharedType struct {
	Name   string
	Native reflect.Type
	Type   *values.Type
}

// SourceFile is one Go source file in a virtual package. Imports are file-local.
type SourceFile struct{ Name, Text string }

type Package struct {
	// StructValues optionally chooses compact storage for source-defined generic
	// structs. A nil result retains ordinary storage. Callbacks run at compile time.
	StructValues map[string]func(string, []*values.Shape) *values.CellStruct
	SourceFiles  []SourceFile
	Aliases      []TypeAlias
	Source       string
	SharedTypes  []SharedType
	Records      []*RecordType
	// EntryTypes are native concrete types that may enter in interface arguments to Run.
	EntryTypes []*values.Type
	Types      []TypeDecl
	Methods    []ExternalMethod
	Path       string
	Functions  []ExternalFunction
	Constants  []ExternalConstant
	Values     []ExternalValue
}

func (p Package) HasSource() bool { return p.Source != "" || len(p.SourceFiles) != 0 }

// Sources returns files in declaration order without sharing the input slice.
// Source, when set, precedes SourceFiles and is named after the package path.
func (p Package) Sources() []SourceFile {
	files := make([]SourceFile, 0, len(p.SourceFiles)+1)
	if p.Source != "" {
		files = append(files, SourceFile{Name: path.Base(p.Path) + ".go", Text: p.Source})
	}
	return append(files, p.SourceFiles...)
}

// MergePackages combines packages that share a path into one, keeping the
// order of first appearance. Compile and Link accept each path once; hosts
// that build one script package from several bindings merge them first.
// Declarations are concatenated; at most one fragment of a path may carry
// Source, and StructValues names must not repeat.
func MergePackages(packages ...Package) ([]Package, error) {
	merged := make([]Package, 0, len(packages))
	index := make(map[string]int, len(packages))
	for _, p := range packages {
		i, ok := index[p.Path]
		if !ok {
			index[p.Path] = len(merged)
			merged = append(merged, p)
			continue
		}
		m := merged[i]
		if m.Source != "" && p.Source != "" {
			return nil, fmt.Errorf("package %q has Source in two fragments", p.Path)
		}
		if m.Source == "" {
			m.Source = p.Source
		}
		if len(p.StructValues) != 0 {
			m.StructValues = maps.Clone(m.StructValues)
			if m.StructValues == nil {
				m.StructValues = map[string]func(string, []*values.Shape) *values.CellStruct{}
			}
			for name, factory := range p.StructValues {
				if m.StructValues[name] != nil {
					return nil, fmt.Errorf("package %q has two struct value rules for %s", p.Path, name)
				}
				m.StructValues[name] = factory
			}
		}
		m.SourceFiles = slices.Concat(m.SourceFiles, p.SourceFiles)
		m.Aliases = slices.Concat(m.Aliases, p.Aliases)
		m.SharedTypes = slices.Concat(m.SharedTypes, p.SharedTypes)
		m.Records = slices.Concat(m.Records, p.Records)
		m.EntryTypes = slices.Concat(m.EntryTypes, p.EntryTypes)
		m.Types = slices.Concat(m.Types, p.Types)
		m.Methods = slices.Concat(m.Methods, p.Methods)
		m.Functions = slices.Concat(m.Functions, p.Functions)
		m.Constants = slices.Concat(m.Constants, p.Constants)
		m.Values = slices.Concat(m.Values, p.Values)
		merged[i] = m
	}
	return merged, nil
}

type linkedFunction struct {
	definedRaw bool
	localReads []uint64
	Function
	params, results Layout
	rSize, pSize    int
}

func (f *linkedFunction) localRead(pc int) bool {
	return f.localReads != nil && f.localReads[pc/64]&(uint64(1)<<uint(pc&63)) != 0
}

type linkedNative struct {
	form         *hostForm
	resultString bool
	record       *RecordType // scoped result of a direct call
	ExternalFunction
	params, results Layout
	rSize, pSize    int
}

func linkNative(fn ExternalFunction, params, results Layout) linkedNative {
	if fn.Direct.Call != nil && fn.Call == nil {
		fn.Call = directWindowCall
	}
	var form *hostForm
	stringResult := false
	if base := fn.Direct.Op.hostForm(); base != nil {
		actual := *base
		stringResult = base.result == values.Pair
		if len(fn.Signature.Results) > 0 {
			actual.result = fn.Signature.Results[0].Repr()
		}
		args := fn.Signature.Params
		if actual.method {
			actual.receiver = args[0].Repr()
			args = args[1:]
		}
		if len(args) > 0 {
			actual.first = args[0].Repr()
		}
		if len(args) > 1 {
			actual.second = args[1].Repr()
		}
		form = &actual
	}
	if fn.Ownership != nil {
		for _, source := range fn.Ownership.Borrowed {
			if source.Result == 0 {
				stringResult = false
			}
		}
	}
	return linkedNative{form: form, resultString: stringResult, ExternalFunction: fn, params: params, results: results, rSize: max(params.RCount, results.RCount), pSize: max(params.PCount, results.PCount)}
}

// Program is an immutable linked snapshot. Registration and symbol lookup stay
// outside the execution loop. Independent VMs may share a Program if its native
// wrappers support concurrent use.
type Program struct {
	mutableFields           sync.Map // mutableFieldKey -> *values.Shape
	hostData                any
	nativeSliceTypes        map[reflect.Type]nativeSliceType
	nativeSliceLayouts      map[*values.NativeLayout]*NativeSliceDecl
	nativeSliceLogicalTypes map[nativeSliceLogicalKey]reflect.Type
	nativeViewMu            sync.Mutex
	nativeViews             atomic.Pointer[nativeViewTable]
	nativeCodecs            map[*values.Type]*NativeValueCodec
	jsonTypes               JSONTypes
	runtimeError, panicNil  uint64
	nativeImports           int
	defers                  []DeferOp
	methodIDs               map[string]uint32
	requirements            map[*values.Shape][]methodRequirement
	shapeByType             map[*values.Type]*values.Shape // the program's own snapshots
	nativeGoShapes          map[reflect.Type]*values.Shape
	nativeModels            map[reflect.Type]nativeModelBinding
	nativeRecordModels      map[*values.Type]*kvo.Model
	types                   []linkedDynamicType
	typeShapes              *values.TypeTable
	typeIDs                 map[*values.Type]uint32
	invokes                 []linkedInvoke
	functionRefs            []FunctionRef
	closures                []Closure
	signatures              []linkedSignature
	functions               []linkedFunction
	natives                 []linkedNative
	words                   []uint64
	strings                 []values.Value
	entries                 map[string]int
	conversions             []Conversion
	shapes                  []*values.Shape
	extendedOps             []ExtendedOp
	recordListSlots         int
	globals                 []*values.Shape
	init                    int
	floatLoops              []floatLoop
	sourceBranches          map[string]map[int]SourceBranch
	// New fields go last: the VM reads the tables above on every
	// instruction, and their places in the struct are part of its timing.
	sharedShapes *sharedShapes // the environment's snapshots, or nil
	sharedSeen   []uint64      // those this program reaches
	// methodNatives are the natives only method tables name, indexed from
	// MethodImportBase: the environment's, shared and never written, or the
	// program's own.
	methodNatives []linkedNative
	// sharedMethodIDs are the method IDs the environment assigned. They
	// come before the program's own in methodIDs.
	sharedMethodIDs map[string]uint32
	borrowRecords   map[*values.Type]RecordType
}

func copySignature(s Signature) Signature {
	return Signature{slices.Clone(s.Params), slices.Clone(s.Results)}
}
func sameSignature(a, b Signature) bool {
	return slices.Equal(a.Params, b.Params) && slices.Equal(a.Results, b.Results)
}

// HostData returns the value supplied when linking. EVM does not interpret it.
// The linker owns this value and must publish any mutable contents safely.
func (p *Program) HostData() any { return p.hostData }

func Link(m *Module, packages ...Package) (*Program, error) {
	return LinkWithHostData(m, nil, packages...)
}

// LinkWithHostData retains host-owned metadata for the lifetime of the linked
// program. There is no setter; derived runtimes can retain their compiled data
// without a separate program registry.
func LinkWithHostData(m *Module, hostData any, packages ...Package) (*Program, error) {
	p, err := newLinkEnvironment(packages, false).link(m, hostData)
	if linkCrossCheck != nil {
		linkCrossCheck(m, hostData, packages, p, err)
	}
	return p, err
}

// linkCrossCheck is set by the evmcrosscheck build; see link_crosscheck.go.
var linkCrossCheck func(m *Module, hostData any, packages []Package, p *Program, err error)

func (e *LinkEnvironment) link(m *Module, hostData any) (*Program, error) {
	if e.packagesErr != nil {
		return nil, e.packagesErr
	}
	packages := e.packages
	if m == nil {
		return nil, fmt.Errorf("nil module")
	}
	// References to the two import lists share one index space.
	if len(m.Functions) == 0 || uint64(len(m.Functions)) > 1<<32 || uint64(len(m.Imports)) > MethodImportBase-nativeErrorNatives-panicNatives || uint64(len(m.MethodImports)) >= nativeFunctionIndex-MethodImportBase {
		return nil, fmt.Errorf("invalid function/import count")
	}
	p := &Program{hostData: hostData, jsonTypes: m.JSONTypes, sharedMethodIDs: e.methodIDs, methodIDs: make(map[string]uint32), requirements: make(map[*values.Shape][]methodRequirement), typeIDs: make(map[*values.Type]uint32), words: slices.Clone(m.Words), entries: make(map[string]int), conversions: slices.Clone(m.Conversions)}
	if e.codecsErr != nil {
		return nil, e.codecsErr
	}
	p.nativeCodecs = e.nativeCodecs
	p.init = -1
	p.nativeImports = len(m.Imports)
	shapes := newShapeSnapshots(e.shapes)
	// A module lists the shapes its instructions index first and then the
	// shapes of the host types it can reach, a few thousand on a large
	// host. The program knows all of them by type and keeps a table of
	// those an instruction can index.
	indexed := 0
	for i := range m.Functions {
		for _, ins := range m.Functions[i].Code {
			switch ins.Op() {
			case OpAlloc:
				indexed = max(indexed, int(ins.B())+1)
			case OpLoad, OpStore:
				indexed = max(indexed, int(ins.C())+1)
			}
		}
	}
	indexed = min(indexed, len(m.Shapes))
	p.shapes = make([]*values.Shape, 0, indexed)
	p.globals = make([]*values.Shape, 0, len(m.Globals))
	p.extendedOps = make([]ExtendedOp, 0, len(m.ExtendedOps))
	p.strings = make([]values.Value, 0, len(m.Strings))
	p.functions = make([]linkedFunction, 0, len(m.Functions))
	// Native error and panic types add their methods after the imports.
	natives := len(m.Imports) + panicNatives
	if len(m.Types) > 1 {
		natives += nativeErrorNatives
	}
	p.natives = make([]linkedNative, 0, natives)
	for i, word := range m.HostShapes.Set {
		if word != 0 && i*64+bits.Len64(word) > len(m.HostShapes.Table) {
			return nil, fmt.Errorf("invalid host shape selection")
		}
	}
	for s := range m.ListedShapes() {
		c, err := shapes.snapshot(s)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, fmt.Errorf("nil storage shape")
		}
	}
	// The table holds the linked form of the first shapes of the module,
	// whatever the order of the listing.
	for _, s := range m.Shapes[:indexed] {
		c, err := shapes.snapshot(s)
		if err != nil {
			return nil, err
		}
		p.shapes = append(p.shapes, c)
	}
	for _, s := range m.Globals {
		c, err := shapes.snapshot(s)
		if err != nil {
			return nil, err
		}
		if c == nil {
			return nil, fmt.Errorf("nil storage shape")
		}
		p.globals = append(p.globals, c)
	}
	if len(m.Shapes) > 65536 {
		return nil, fmt.Errorf("too many storage shapes")
	}
	for _, op := range m.ExtendedOps {
		if op.Kind == ExtendedGetKey || op.Kind == ExtendedSetKey || op.Kind == ExtendedGetChildKey || op.Kind == ExtendedRecordList || op.Kind == ExtendedRecordListLen || op.Kind == ExtendedRecordListIndex {
			if !e.records[op.Record] {
				return nil, fmt.Errorf("missing KVO record declaration")
			}
			frozen := *op.Record
			op.Record = &frozen
			if frozen.Model == nil {
				return nil, fmt.Errorf("missing KVO model for %s", frozen.Name)
			}
			prop := frozen.Model.PropByCode(op.Index)
			if prop == nil {
				return nil, fmt.Errorf("unknown KVO property %d", op.Index)
			}
			flags := frozen.Model.PropFlags(op.Index)
			op.step = row2.Step{Key: op.Index, Options: kvo.WriteOptions(flags)}
			op.childModel = prop.TypeModel()
			if list, ok := prop.AnyType().(*kvo.ListType); ok && !frozen.Mutable && op.Kind == ExtendedGetKey {
				if _, objects := list.ItemType().(*kvo.EntityType); objects {
					op.listAccess = kvo.BindListAccess(frozen.Model, op.Index)
				}
			}
		}
		var err error
		op.Shape, err = shapes.snapshot(op.Shape)
		if err != nil {
			return nil, err
		}
		op.Source, err = shapes.snapshot(op.Source)
		if err != nil {
			return nil, err
		}
		op.In = slices.Clone(op.In)
		op.Out = slices.Clone(op.Out)
		if op.Kind == ExtendedRecordList || op.Kind == ExtendedRecordListLen || op.Kind == ExtendedRecordListIndex {
			if _, ok := op.Record.Model.PropByCode(op.Index).AnyType().(*kvo.ListType); !ok {
				return nil, fmt.Errorf("KVO property %d is not a list", op.Index)
			}
			op.listAccess = kvo.BindListAccess(op.Record.Model, op.Index)
		}
		if op.Kind == ExtendedRecordListIndex && op.Flags&RecordListDirect == 0 {
			op.listSlot = p.recordListSlots
			p.recordListSlots++
		}
		p.extendedOps = append(p.extendedOps, op)
	}
	shapeStates := make(map[*values.Shape]shapeValidationState)
	for _, s := range shapes.order {
		if !validateStorageShape(s, shapeStates, e.shapes) {
			return nil, fmt.Errorf("invalid storage layout for %s", s.Type.Name())
		}
	}
	if len(p.conversions) > 65536 {
		return nil, fmt.Errorf("too many numeric conversions")
	}
	for _, c := range p.conversions {
		if !c.From.valid() || !c.To.valid() {
			return nil, fmt.Errorf("invalid numeric conversion")
		}
	}
	for _, s := range m.Strings {
		p.strings = append(p.strings, values.String(s))
	}
	if e.bindingsErr != nil {
		return nil, e.bindingsErr
	}
	for i := range m.Imports {
		p.natives = append(p.natives, linkedNative{})
		if _, err := e.linkImport(&m.Imports[i], shapes, &p.natives[i]); err != nil {
			return nil, err
		}
	}
	if len(m.SourceBranches) != 0 {
		p.sourceBranches = make(map[string]map[int]SourceBranch, len(m.SourceBranches))
	}
	for _, f := range m.Functions {
		if _, ok := p.entries[f.Name]; ok || f.Name == "" {
			return nil, fmt.Errorf("empty or duplicate function %q", f.Name)
		}
		args, err := ParameterLayout(f.Signature.Params, f.Method)
		if err != nil {
			return nil, err
		}
		results, err := LayoutOf(f.Signature.Results)
		if err != nil {
			return nil, err
		}
		if f.RCount < max(args.RCount, results.RCount) || f.PCount < max(args.PCount, results.PCount) || f.RCount > 65536 || f.PCount > 65536 {
			return nil, fmt.Errorf("%s: invalid register counts", f.Name)
		}
		if uint64(f.RecoverPC) > uint64(len(f.Code)) {
			return nil, fmt.Errorf("invalid recover PC")
		}
		if len(f.Code) == 0 || (len(f.Lines) != 0 && len(f.Lines) != len(f.Code)) || (len(f.Positions) != 0 && len(f.Positions) != len(f.Code)) {
			return nil, fmt.Errorf("%s: invalid code/line count", f.Name)
		}
		for pc, branch := range m.SourceBranches[f.Name] {
			if pc < 0 || pc >= len(f.Code) || branch.PC < 0 || branch.PC >= len(f.Code) || !coverageBranch(f.Code[pc].Op()) || !coverageBranch(f.Code[branch.PC].Op()) {
				return nil, fmt.Errorf("%s: invalid source branch at %d", f.Name, pc)
			}
		}
		f.Signature = copySignature(f.Signature)
		f.Code, f.Lines = slices.Clone(f.Code), slices.Clone(f.Lines)
		var localReads []uint64
		if len(f.LocalRecordReads) != 0 {
			localReads = make([]uint64, (len(f.Code)+63)/64)
			for pc, local := range f.LocalRecordReads {
				if local && pc >= 0 && pc < len(f.Code) {
					localReads[pc/64] |= 1 << uint(pc&63)
				}
			}
		}
		f.LocalRecordReads = nil
		f.Positions = slices.Clone(f.Positions)
		if branches := m.SourceBranches[f.Name]; len(branches) != 0 {
			p.sourceBranches[f.Name] = maps.Clone(branches)
		}
		f.Captures = slices.Clone(f.Captures)
		p.entries[f.Name] = len(p.functions)
		p.functions = append(p.functions, linkedFunction{Function: f, localReads: localReads, params: args, results: results, rSize: f.RCount, pSize: f.PCount})
	}
	for name := range m.SourceBranches {
		if _, ok := p.entries[name]; !ok {
			return nil, fmt.Errorf("source branches for unknown function %q", name)
		}
	}
	if err := p.linkFunctions(m); err != nil {
		return nil, err
	}
	if err := p.linkInterfaces(e, m, shapes); err != nil {
		return nil, err
	}
	if err := p.checkJSONTypes(); err != nil {
		return nil, err
	}
	shapeStates = make(map[*values.Shape]shapeValidationState)
	for _, s := range shapes.order {
		if !validateStorageShape(s, shapeStates, e.shapes) {
			return nil, fmt.Errorf("invalid storage layout for %s", s.Type.Name())
		}
	}
	if err := p.linkNativeErrors(shapes); err != nil {
		return nil, err
	}
	if err := p.linkPanicTypes(); err != nil {
		return nil, err
	}
	types := make([]values.DynamicType, len(p.types))
	for id := 1; id < len(types); id++ {
		s := p.types[id].shape
		types[id] = values.DynamicType{Shape: s, Comparable: s.Comparable()}
	}
	p.typeShapes = values.NewTypeTable(types)
	for i := range p.extendedOps {
		op := &p.extendedOps[i]
		if op.Kind == ExtendedTypeAssert && op.Shape != nil {
			op.Index = uint64(p.typeIDs[op.Shape.Type])
		}
	}
	if err := p.linkDefers(m); err != nil {
		return nil, err
	}
	// The program lists its own snapshots by type and finds the shared ones
	// it reaches through the environment; see Shape.
	p.sharedShapes, p.sharedSeen = e.shapes, shapes.seen
	p.shapeByType = make(map[*values.Type]*values.Shape, len(shapes.own))
	p.nativeGoShapes = make(map[reflect.Type]*values.Shape)
	concrete := func(s *values.Shape) error {
		if p.typeIDs[s.Type] == 0 {
			return nil
		}
		typ := s.Native.GoType
		if old := p.nativeGoShapes[typ]; old != nil && old.Type != s.Type && typ.Kind() == reflect.Pointer && (old.Type.Repr() == values.Pointer || s.Type.Repr() == values.Pointer) {
			return fmt.Errorf("conflicting opaque native pointer identities for %v: %s and %s", typ, old.Type.Name(), s.Type.Name())
		}
		if old := p.nativeGoShapes[typ]; old == nil || s.Type.Name() < old.Type.Name() {
			p.nativeGoShapes[typ] = s
		}
		return nil
	}
	required := func(s *values.Shape) error {
		for _, m := range s.Methods {
			p.requirements[s] = append(p.requirements[s], methodRequirement{p.methodID(m.ID), m})
		}
		return nil
	}
	for _, s := range shapes.order {
		if p.shapeByType[s.Type] == nil {
			p.shapeByType[s.Type] = s
		}
		if s.Kind == values.InterfaceKind {
			required(s)
		} else if s.Native != nil && s.Native.GoType != nil {
			if err := concrete(s); err != nil {
				return nil, err
			}
		}
	}
	if e.shapes != nil {
		if err := shapes.eachShared(e.shapes.nativeGo, concrete); err != nil {
			return nil, err
		}
		shapes.eachShared(e.shapes.interfaces, required)
	}
	if err := p.linkNativeModels(packages); err != nil {
		return nil, err
	}
	if e.nativeSlicesErr != nil {
		return nil, e.nativeSlicesErr
	}
	p.linkNativeSlices(e)
	for i := range p.functions {
		f := &p.functions[i]
		for pc, ins := range f.Code {
			switch ins.Op() {
			case OpCallFunc, OpCallHost, OpCallHostRR, OpCallHostRP, OpCallIndirect, OpCallVirt, OpDefer:
			default:
				if ins.Op().hostForm() == nil {
					continue
				}
			}
			r, ptr, ok := p.callSize(ins)
			if !ok {
				return nil, fmt.Errorf("%s pc %d: invalid call target", f.Name, pc)
			}
			f.rSize = max(f.rSize, f.RCount+r)
			f.pSize = max(f.pSize, f.PCount+ptr)
		}
		if f.rSize > 65536 || f.pSize > 65536 {
			return nil, fmt.Errorf("%s: locals and outgoing window exceed register address range", f.Name)
		}
		for pc, ins := range f.Code {
			if err := p.validate(f, pc, ins); err != nil {
				return nil, fmt.Errorf("%s pc %d: %w", f.Name, pc, err)
			}
			if !m.NoOptimization && pc > 0 && ins.Op() == OpExtended && f.Code[pc-1].Op() == OpExtended {
				first := f.Code[pc-1]
				left, right := &p.extendedOps[first.BC()], &p.extendedOps[ins.BC()]
				if left.Kind == ExtendedGetChildKey && right.Kind == ExtendedGetChildKey && left.Record.Model == right.Record.Model && left.Index == right.Index && left.In[0] == right.In[0] {
					f.Code[pc-1] = WithBC(opReadChildPair, 0, first.BC())
				}
			}
			if !m.NoOptimization && pc > 0 {
				prev := f.Code[pc-1]
				if prev.Op() == opCallDirect && prev.A() == ins.A() {
					repr := p.natives[prev.C()].form.result
					if repr == values.Raw && (ins.Op() == OpJz || ins.Op() == OpJnz) || repr == values.Pointer && (ins.Op() == OpJNil || ins.Op() == OpJNotNil) {
						f.Code[pc-1] = ABC(opTestDirect, prev.A(), prev.B(), prev.C())
					}
				}
			}
			// A direct jump to the second word must still execute the branch.
			if !m.NoOptimization && pc > 0 && (ins.Op() == OpJz || ins.Op() == OpJnz) {
				cmp := f.Code[pc-1]
				if cmp.A() == ins.A() {
					var op Opcode
					switch cmp.Op() {
					case OpLtI64:
						op = opBranchLtI64
					case OpLeI64:
						op = opBranchLeI64
					case OpLtF64:
						op = opBranchLtF64
					case OpEqR:
						op = opBranchEqR
					}
					if op != 0 {
						f.Code[pc-1] = ABC(op, cmp.A(), cmp.B(), cmp.C())
					}
				}
			}
			if !m.NoOptimization && ins.Op() == OpExtended {
				op := &p.extendedOps[ins.BC()]
				s := op.Shape
				// Mirror nativeLoadedValue only when no codec can change the pointer.
				if op.Kind == ExtendedAddressLoad && s.Kind == values.PointerKind && s.Type.Repr() == values.Pair && s.Native != nil && s.Native.Load != nil && s.Native.Codec == nil && p.nativeCodecs[s.Type] == nil {
					f.Code[pc] = WithBC(opNativePointerLoad, 0, ins.BC())
				}
			}
			if ins.Op() == OpCallHostUU && p.natives[ins.C()].Direct.word != nil {
				f.Code[pc] = ABC(opCallRawUnary, ins.A(), ins.B(), ins.C())
			} else if ins.Op().hostForm() != nil {
				f.Code[pc] = ABC(opCallDirect, ins.A(), ins.B(), ins.C())
			}
		}
		if !m.NoOptimization {
			if m.StrictCallWindows {
				f.definedRaw = p.rawFrameDefined(f, m.Functions[i].Code)
			}
			p.linkFloatLoops(f)
		}
	}
	if m.Init != "" {
		i, ok := p.entries[m.Init]
		if !ok {
			return nil, fmt.Errorf("missing package init")
		}
		f := p.functions[i]
		if len(f.Signature.Params) != 0 || len(f.Signature.Results) != 0 {
			return nil, fmt.Errorf("invalid package init signature")
		}
		p.init = i
	}
	for i := range p.functions {
		f := &p.functions[i]
		if f.Internal || f.Name == m.Init {
			delete(p.entries, f.Name)
		}
	}
	return p, nil
}

// linkImport binds one import of a module to what the packages export and
// writes the binding to dst. A prepared environment answers with the binding
// it resolved when it was built: prepared is its index in e.natives, or -1
// for a binding resolved for this import alone, as a generic one is.
func (e *LinkEnvironment) linkImport(imp *Import, shapes *shapeSnapshots, dst *linkedNative) (prepared int, _ error) {
	// Copy the declaration: the host owns it, and a program keeps its
	// own signature and effects.
	var f ExternalFunction
	export, ok := e.exports[exportKey{imp.Package, imp.Name, imp.Receiver}]
	resolved, errs := export.resolved, export.errs
	if resolved != nil {
		f = resolved.ExternalFunction
	} else if ok {
		f = *export.declared
	} else if imp.Receiver == nil {
		if standard, found := BindStandardFunction(imp.Package, imp.Name, imp.Signature); found {
			if err := validateStandardSignature(imp, shapes); err != nil {
				return -1, err
			}
			f, ok = *standard, true
		}
	}
	if !ok {
		return -1, fmt.Errorf("unresolved import %s.%s", imp.Package, imp.Name)
	}
	if f.Instantiate != nil {
		name := f.Name
		var err error
		for _, arg := range imp.TypeArgs {
			if arg == nil {
				return -1, fmt.Errorf("nil type argument for %s.%s", imp.Package, imp.Name)
			}
		}
		f, err = f.Instantiate(slices.Clone(imp.TypeArgs))
		f.Name = name
		if err != nil {
			return -1, fmt.Errorf("%s.%s: %w", imp.Package, imp.Name, err)
		}
		if f.Instantiate != nil || f.Call == nil && f.Direct.Call == nil || f.Direct.Op != 0 && f.Direct.Call == nil {
			return -1, fmt.Errorf("invalid generic binding %s.%s", imp.Package, imp.Name)
		}
		if f.Direct.Op != 0 || f.Direct.Call != nil {
			instance := Package{Path: imp.Package}
			if imp.Receiver == nil {
				instance.Functions = []ExternalFunction{f}
			} else {
				if len(f.Signature.Params) == 0 || f.Signature.Params[0] != imp.Receiver {
					return -1, fmt.Errorf("generic method receiver mismatch")
				}
				instance.Methods = []ExternalMethod{{Name: imp.Name, Receiver: imp.Receiver, Signature: Signature{Params: f.Signature.Params[1:], Results: f.Signature.Results}, Direct: f.Direct, Variadic: f.Variadic}}
			}
			// The host types of the prepared packages decide which
			// register forms fit the instance.
			if err := validateDirectBindings([]Package{instance}, e.hostKinds); err != nil {
				return -1, err
			}
		}
	} else if len(imp.TypeArgs) != 0 {
		return -1, fmt.Errorf("unexpected type arguments for %s.%s", imp.Package, imp.Name)
	}
	prepared = -1
	if resolved != nil {
		prepared = int((uintptr(unsafe.Pointer(resolved)) - uintptr(unsafe.Pointer(unsafe.SliceData(e.natives)))) / unsafe.Sizeof(linkedNative{}))
		*dst = *resolved
	} else {
		errs = nil
		if ownership, layout := e.resolve(dst, f, imp.Receiver != nil); ownership != nil || layout != nil {
			errs = &resolveErrors{ownership, layout}
		}
	}
	if errs != nil && errs.ownership != nil {
		return -1, fmt.Errorf("%s.%s: %w", imp.Package, imp.Name, errs.ownership)
	}
	if !imp.Ownership.Equal(dst.Ownership) {
		return -1, fmt.Errorf("ownership effects mismatch for %s.%s", imp.Package, imp.Name)
	}
	if !sameSignature(imp.Signature, dst.Signature) {
		return -1, fmt.Errorf("signature mismatch for %s.%s", imp.Package, imp.Name)
	}
	if errs != nil && errs.layout != nil {
		return -1, errs.layout
	}
	return prepared, nil
}

// UnaryHostOpcode selects the fused argument move, external call and result move.
func UnaryHostOpcode(sig Signature) Opcode {
	if len(sig.Params) == 1 && len(sig.Results) == 1 && sig.Params[0].Repr() == values.Raw {
		switch sig.Results[0].Repr() {
		case values.Raw:
			return OpCallHostRR
		case values.Pointer:
			return OpCallHostRP
		}
	}
	return 0
}

func (p *Program) callSize(i Instruction) (r, ptr int, ok bool) {
	var params, results Layout
	if i.Op().hostForm() != nil {
		if int(i.C()) >= p.nativeImports {
			return 0, 0, false
		}
		g := &p.natives[i.C()]
		if i.Op() != g.Direct.Op {
			return 0, 0, false
		}
		return 0, 0, true
	}
	switch i.Op() {
	case OpDefer:
		if uint64(i.BC()) >= uint64(len(p.defers)) {
			return 0, 0, false
		}
		r, ptr := p.deferSize(&p.defers[i.BC()])
		return r, ptr, true
	case OpCallVirt:
		if uint64(i.BC()) >= uint64(len(p.invokes)) {
			return 0, 0, false
		}
		op := &p.invokes[i.BC()]
		return op.rSize, op.pSize, true
	case OpCallIndirect:
		if uint64(i.BC()) >= uint64(len(p.signatures)) {
			return 0, 0, false
		}
		s := p.signatures[i.BC()]
		params, results = s.params, s.results
	case OpCallFunc:
		if uint64(i.BC()) >= uint64(len(p.functions)) {
			return 0, 0, false
		}
		g := &p.functions[i.BC()]
		if len(g.Captures) > 0 {
			return 0, 0, false
		}
		params, results = g.params, g.results
	case OpCallHost, OpCallHostRR, OpCallHostRP:
		index := i.BC()
		if i.Op() != OpCallHost {
			index = uint32(i.C())
		}
		if uint64(index) >= uint64(p.nativeImports) {
			return 0, 0, false
		}
		g := &p.natives[index]
		if i.Op() != OpCallHost && i.Op() != UnaryHostOpcode(g.Signature) {
			return 0, 0, false
		}
		params, results = g.params, g.results
	default:
		return 0, 0, false
	}
	return max(params.RCount, results.RCount), max(params.PCount, results.PCount), true
}

func (p *Program) validate(f *linkedFunction, pc int, i Instruction) error {
	a, b, c := int(i.A()), int(i.B()), int(i.C())
	r := func(n int) bool { return n < f.rSize }
	ptr := func(n int) bool { return n < f.pSize }
	v := func(n int) bool { return r(n) && ptr(n) }
	valid := false
	switch i.Op() {
	case OpDeferStack:
		valid = r(a) && b == 0 && c == 0
	case OpRunDefers:
		valid = a == 0 && b == 0 && c == 0
	case OpPanic, OpRecover:
		valid = v(a) && b == 0 && c == 0
	case OpDefer:
		if uint64(i.BC()) < uint64(len(p.defers)) {
			d := &p.defers[i.BC()]
			valid = (!d.ExternalStack && a == 0 || d.ExternalStack && r(a)) && p.validDefer(f, d)
		}
	case OpCallVirt:
		valid = a == 0 && uint64(i.BC()) < uint64(len(p.invokes))
	case OpFunction:
		valid = v(a) && uint64(i.BC()) < uint64(len(p.functionRefs))
		if valid {
			ref := p.functionRefs[i.BC()]
			valid = ref.Native || len(p.functions[ref.Index].Captures) == 0
		}
	case OpClosure:
		if v(a) && uint64(i.BC()) < uint64(len(p.closures)) {
			valid = true
			for _, r := range p.closures[i.BC()].Captures {
				if r.Repr < values.Raw || r.Repr > values.Pair || r.Repr != values.Pointer && !rValid(r, f) || r.Repr != values.Raw && !pValid(r, f) {
					valid = false
				}
			}
		}
	case OpCallIndirect:
		_, _, ok := p.callSize(i)
		valid = v(a) && ok
	case OpCheckNil:
		valid = ptr(a) && b == 0 && c == 0
	case OpAlloc:
		valid = ptr(a) && b < len(p.shapes) && c == 0
	case OpGlobal:
		valid = ptr(a) && uint64(i.BC()) < uint64(len(p.globals))
	case OpMulAddF64, OpAddMulF64, OpSubAddF64:
		a, b, c, d := i.ABCD12()
		valid = r(int(a)) && r(int(b)) && r(int(c)) && r(int(d))
	case OpAddImmI64:
		valid = r(a) && r(b)
	case OpLoadIndexPtr:
		valid = ptr(a) && v(b) && r(c)
	case OpStoreIndexPtr:
		valid = v(a) && r(b) && ptr(c)
	case OpLoadIndexR, OpLoadIndexByte, OpLoadIndexWord:
		valid = r(a) && v(b) && r(c)
	case OpStoreIndexR, OpStoreIndexByte, OpStoreIndexWord:
		valid = v(a) && r(b) && r(c)
	case OpLeI64, OpLeU64:
		valid = r(a) && r(b) && r(c)
	case OpLoad, OpStore:
		if c < len(p.shapes) {
			repr := p.shapes[c].Type.Repr()
			valueReg, pointerReg := a, b
			if i.Op() == OpStore {
				valueReg, pointerReg = b, a
			}
			valid = ptr(pointerReg) && (repr == values.Raw && r(valueReg) || repr == values.Pointer && ptr(valueReg) || repr == values.Pair && v(valueReg))
		}
	case OpExtended:
		valid = a == 0 && uint64(i.BC()) < uint64(len(p.extendedOps)) && p.validateExtended(f, p.extendedOps[i.BC()])
	case OpConvert:
		valid = r(a) && r(b) && c < len(p.conversions)
	case OpCheckShift:
		valid = r(a) && b == 0 && c == 0
	case OpConcatStr:
		valid = v(a) && v(b) && v(c)
	case OpEqStr, OpLtStr:
		valid = r(a) && v(b) && v(c)
	case OpAddF32, OpSubF32, OpMulF32, OpDivF32, OpEqF32, OpLtF32:
		valid = r(a) && r(b) && r(c)
	case OpConst:
		valid = r(a) && uint64(i.BC()) < uint64(len(p.words))
	case OpString:
		valid = v(a) && uint64(i.BC()) < uint64(len(p.strings))
	case OpMoveR:
		valid = r(a) && r(b) && c == 0
	case OpMoveP:
		valid = ptr(a) && ptr(b) && c == 0
	case OpMoveV:
		valid = v(a) && v(b) && c == 0
	case OpClearP:
		valid = ptr(a) && b == 0 && c == 0
	case OpEqP:
		valid = r(a) && ptr(b) && ptr(c)
	case OpAddI64, OpSubI64, OpMulI64, OpDivI64, OpDivU64, OpRemI64, OpRemU64,
		OpAddF64, OpSubF64, OpMulF64, OpDivF64, OpAnd, OpOr, OpXor, OpShl,
		OpShrI64, OpShrU64, OpEqR, OpLtI64, OpLtU64, OpEqF64, OpLtF64:
		valid = r(a) && r(b) && r(c)
	case OpCallHostRR:
		valid = a < f.RCount && b < f.RCount
	case OpCallHostRP:
		valid = a < f.PCount && b < f.RCount
	case OpJNil, OpJNotNil:
		target := int64(pc) + 1 + int64(int32(i.BC()))
		valid = ptr(a) && target >= 0 && target < int64(len(f.Code))
	case OpLoopAddLtI64, OpLoopAddLeI64:
		a, b, c, _ := i.ABCD12()
		target := pc + 1 + i.SignedD12()
		valid = r(int(a)) && r(int(b)) && r(int(c)) && target >= 0 && target < len(f.Code)
	case OpLoopLtI64, OpLoopLeI64:
		target := pc + 1 + int(int16(i.C()))
		valid = r(a) && r(b) && target >= 0 && target < len(f.Code)
	case OpJump, OpJz, OpJnz:
		target := int64(pc) + 1 + int64(int32(i.BC()))
		valid = target >= 0 && target < int64(len(f.Code)) && ((i.Op() == OpJump && a == 0) || (i.Op() != OpJump && r(a)))
	case OpCallFunc, OpCallHost:
		_, _, ok := p.callSize(i)
		valid = a == 0 && ok
	case OpReturn:
		valid = a == 0 && b == 0 && c == 0
	default:
		if h := i.Op().hostForm(); h != nil {
			if c < len(p.natives) && p.natives[c].form != nil {
				h = p.natives[c].form
			}
			valid = h.validSlots(a, b, f)
		}
	}
	if !valid {
		return fmt.Errorf("invalid opcode or operands: %#016x", uint64(i))
	}
	if pc == len(f.Code)-1 && i.Op() != OpReturn && i.Op() != OpPanic && i.Op() != OpJump {
		return fmt.Errorf("execution can fall past end of function")
	}
	return nil
}
