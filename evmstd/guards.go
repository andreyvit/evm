package evmstd

import "bytes"

// maxRepeat bounds output a native function builds outside the VM's budget.
const maxRepeat = 16 << 20

func repeatBytes(b []byte, count int) []byte {
	if count < 0 {
		panic("bytes: negative Repeat count")
	}
	if len(b) != 0 && count > maxRepeat/len(b) {
		panic("bytes: Repeat output is too large")
	}
	return bytes.Repeat(b, count)
}
