package evmstd

import (
	"bytes"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unsafe"

	"github.com/andreyvit/evm"
	"github.com/andreyvit/evm/evmbind"
	"github.com/andreyvit/evm/values"
)

func bindRegexpReplacements(pkg *evm.Package) {
	for i := range pkg.Methods {
		m := &pkg.Methods[i]
		switch m.Name {
		case "ReplaceAll", "ReplaceAllString", "ReplaceAllLiteral", "ReplaceAllLiteralString", "ReplaceAllFunc", "ReplaceAllStringFunc":
			name := m.Name
			m.Direct = evm.DirectBinding{}
			m.Call = func(vm *evm.VM) (err error) {
				defer evmbind.Catch("regexp.Regexp."+name, &err)
				return regexpReplace(vm, name)
			}
		}
	}
}

func regexpBytes(v values.Value) []byte {
	p := evmbind.NativeSliceData(v, reflect.TypeFor[byte]())
	return unsafe.Slice((*byte)(p), int(v.SliceCap()))[:v.SliceLen()]
}

func byteSliceValue(b []byte) values.Value {
	return values.SliceValue(unsafe.Pointer(unsafe.SliceData(b)), uint32(len(b)), uint32(min(uint64(cap(b)), uint64(math.MaxUint32))))
}

// regexpInput borrows immutable string storage for the regexp reader.
// Only byte-slice methods expose writable matches to a callback.
func regexpInput(v values.Value, isString bool) []byte {
	if isString {
		return v.AsBytes()
	}
	return regexpBytes(v)
}

func regexpReplace(vm *evm.VM, name string) error {
	receiver := vm.CallArgument(0)
	if receiver.P != nil && receiver.R != values.NativeAddress {
		return evm.ErrInvalidState
	}
	re := (*regexp.Regexp)(receiver.P)
	isString := strings.Contains(name, "String")
	src := regexpInput(vm.CallArgument(1), isString)
	arg := vm.CallArgument(2)
	var result []byte
	if !re.Match(src) {
		chargeBytes(vm, len(src))
		result = append([]byte(nil), src...)
	} else if strings.HasSuffix(name, "Func") {
		var fn evm.Callable
		if arg.R != 0 {
			var err error
			fn, err = vm.Program().Callable(arg)
			if err != nil {
				return err
			}
		}
		result = replaceRegexpFunc(vm, re, src, isString, fn)
	} else {
		repl := regexpInput(arg, isString)
		literal := strings.Contains(name, "Literal") || bytes.IndexByte(repl, '$') < 0
		result = replaceRegexpText(vm, re, src, repl, literal)
	}
	if isString {
		// The replacement buffer is newly allocated and has no writable aliases.
		vm.SetCallResult(0, values.String(unsafe.String(unsafe.SliceData(result), len(result))))
	} else {
		vm.SetCallResult(0, byteSliceValue(result))
	}
	return nil
}

// Keep Go's match loop: a byte callback can edit the input before the next
// search. Copy each unmatched prefix before invoking guest code, and copy the
// callback result before the next invocation can change it.
func replaceRegexpFunc(vm *evm.VM, re *regexp.Regexp, src []byte, isString bool, fn evm.Callable) []byte {
	var out []byte
	appendPiece := func(piece []byte) {
		size := addRegexpBytes(len(out), len(piece))
		if size > cap(out) {
			capacity := max(size, int(min(2*uint64(cap(out)), uint64(math.MaxUint32), uint64(math.MaxInt))))
			chargeBytes(vm, capacity)
			buf := make([]byte, len(out), capacity)
			copy(buf, out)
			out = buf
		}
		out = append(out, piece...)
	}
	last := 0
	re.ReplaceAllFunc(src, func(m []byte) []byte {
		// regexp passes src[start:end], with the original remaining capacity.
		start := cap(src) - cap(m)
		appendPiece(src[last:start])
		last = start + len(m)
		var in values.Value
		if isString {
			in = values.String(unsafe.String(unsafe.SliceData(m), len(m)))
		} else {
			in = byteSliceValue(m)
		}
		slots := [2]values.Value{in}
		if err := vm.Run(fn, slots[:]); err != nil {
			vm.PanicCallback(err)
		}
		appendPiece(regexpInput(slots[1], isString))
		return nil
	})
	appendPiece(src[last:])
	return out
}

func addRegexpBytes(n, add int) int {
	if uint64(n)+uint64(add) > min(uint64(math.MaxInt), uint64(math.MaxUint32)) {
		panic(evm.ErrAllocation)
	}
	return n + add
}

func replaceRegexpText(vm *evm.VM, re *regexp.Regexp, src, repl []byte, literal bool) []byte {
	// Count with the original regexp and the full input. Searching suffixes
	// would change the meaning of anchors and word boundaries.
	count, unmatched := 0, len(src)
	re.ReplaceAllFunc(src, func(m []byte) []byte {
		count++
		unmatched -= len(m)
		return nil
	})
	if literal {
		chargeBytes(vm, addRepeatedBytes(unmatched, len(repl), count))
		return re.ReplaceAllLiteral(src, repl)
	}

	// FindAllSubmatchIndex allocates two indexes per group for each match.
	// Reserve its table before asking Go for it.
	stride := (2*(uint64(re.NumSubexp())+1) + 3) * uint64(unsafe.Sizeof(int(0)))
	if uint64(count) > math.MaxUint64/stride {
		panic(evm.ErrAllocation)
	}
	if err := vm.ChargeAllocation(uint64(count) * stride); err != nil {
		panic(err)
	}
	matches := re.FindAllSubmatchIndex(src, count)
	template := unsafe.String(unsafe.SliceData(repl), len(repl))
	size := regexpExpansionSize(unmatched, template, matches, re.SubexpNames())
	chargeBytes(vm, size)
	var result []byte
	if size > 0 {
		result = make([]byte, 0, size)
	}
	source := unsafe.String(unsafe.SliceData(src), len(src))
	last := 0
	for _, m := range matches {
		result = append(result, src[last:m[0]]...)
		result = re.ExpandString(result, template, source, m)
		last = m[1]
	}
	return append(result, src[last:]...)
}

// Go consumes the longest name after a dollar, including braces when present.
// Only a participating submatch contributes bytes.
var regexpReference = regexp.MustCompile(`^\$(?:\$|\{([\pL\p{Nd}_]+)\}|([\pL\p{Nd}_]+))`)

func regexpExpansionSize(size int, template string, matches [][]int, names []string) int {
	widths := make(map[string]int)
	for {
		i := strings.IndexByte(template, '$')
		if i < 0 {
			return addRepeatedBytes(size, len(template), len(matches))
		}
		size = addRepeatedBytes(size, i, len(matches))
		template = template[i:]
		ref := regexpReference.FindStringSubmatchIndex(template)
		if ref == nil {
			size = addRegexpBytes(size, len(matches))
			template = template[1:]
			continue
		}
		token := template[:ref[1]]
		template = template[ref[1]:]
		if token == "$$" {
			size = addRegexpBytes(size, len(matches))
			continue
		}
		a, b := ref[2], ref[3]
		if a < 0 {
			a, b = ref[4], ref[5]
		}
		name := token[a:b]
		width, ok := widths[name]
		if !ok {
			width = regexpReferenceWidth(name, matches, names)
			widths[name] = width
		}
		size = addRegexpBytes(size, width)
	}
}

// Submatch spans from different matches cannot overlap, so their total width
// fits in the input length. Duplicate names select the first participating group.
func regexpReferenceWidth(name string, matches [][]int, names []string) int {
	width := 0
	index, err := strconv.ParseUint(name, 10, 30)
	if err == nil && index < 1e9 && (len(name) == 1 || name[0] != '0') {
		if index < uint64(len(names)) {
			for _, m := range matches {
				if m[2*index] >= 0 {
					width += m[2*index+1] - m[2*index]
				}
			}
		}
		return width
	}
	for _, m := range matches {
		for j, n := range names {
			if n == name && m[2*j] >= 0 {
				width += m[2*j+1] - m[2*j]
				break
			}
		}
	}
	return width
}

func addRepeatedBytes(n, width, count int) int {
	if count > 0 && width > (math.MaxInt-n)/count {
		panic(evm.ErrAllocation)
	}
	return addRegexpBytes(n, width*count)
}
