package evmstd

import (
	"bytes"
	"math"

	"github.com/andreyvit/evm"
)

const maxRepeat = 16 << 20

type byteGuards struct{}

func chargeBytes(vm *evm.VM, n int) {
	if uint64(n) > math.MaxUint32 {
		panic(evm.ErrAllocation)
	}
	if err := vm.ChargeAllocation(uint64(n)); err != nil {
		panic(err)
	}
}

func (byteGuards) Repeat(vm *evm.VM, b []byte, count int) []byte {
	if count < 0 {
		panic("bytes: negative Repeat count")
	}
	if len(b) != 0 && count > maxRepeat/len(b) {
		panic("bytes: Repeat output is too large")
	}
	chargeBytes(vm, len(b)*count)
	return bytes.Repeat(b, count)
}

func (byteGuards) ReplaceAll(vm *evm.VM, s, old, repl []byte) []byte {
	return replaceBytes(vm, s, old, repl, -1)
}

func (byteGuards) Replace(vm *evm.VM, s, old, repl []byte, limit int) []byte {
	return replaceBytes(vm, s, old, repl, limit)
}

func replaceBytes(vm *evm.VM, s, old, repl []byte, limit int) []byte {
	count := 0
	switch limit {
	case 0:
		// bytes.Replace still copies s.
	case 1:
		if bytes.Contains(s, old) {
			count = 1
		}
	default:
		count = bytes.Count(s, old)
		if limit > 0 {
			count = min(count, limit)
		}
	}
	delta := len(repl) - len(old)
	if delta > 0 && count > (math.MaxInt-len(s))/delta {
		panic(evm.ErrAllocation)
	}
	chargeBytes(vm, len(s)+count*delta)
	return bytes.Replace(s, old, repl, limit)
}

func (byteGuards) Join(vm *evm.VM, parts [][]byte, sep []byte) []byte {
	size := 0
	if len(parts) > 1 {
		if len(sep) > math.MaxInt/(len(parts)-1) {
			panic(evm.ErrAllocation)
		}
		size = (len(parts) - 1) * len(sep)
	}
	for _, part := range parts {
		if len(part) > math.MaxInt-size {
			panic(evm.ErrAllocation)
		}
		size += len(part)
	}
	chargeBytes(vm, size)
	return bytes.Join(parts, sep)
}
