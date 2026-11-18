//go:build !evmreflect

package evmbind_test

import (
	"github.com/andreyvit/evm/evmbind/internal/timebindings"
	"testing"
)

func TestNativeTimeSourceGenerated(t *testing.T) { testNativeTimeSource(t, &timebindings.Bindings) }
