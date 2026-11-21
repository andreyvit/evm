package evmbind

import (
	"bytes"
	"reflect"
	"testing"
	"testing/fstest"
)

type BorrowedByteFixture struct{}

func (*BorrowedByteFixture) Read() []byte { return []byte("public") }
func (*BorrowedByteFixture) View() []byte { return []byte("borrowed") }

func TestBorrowedByteMethodBinding(t *testing.T) {
	const source = "package evmbind\ntype BorrowedByteFixture struct{}\n//evm:borrow-bytes test.tx View\nfunc (*BorrowedByteFixture) Read() []byte {panic(0)}"
	files := fstest.MapFS{"api.go": {Data: []byte(source)}}
	opt := Options{Only: []string{"BorrowedByteFixture"}, Types: []reflect.Type{reflect.TypeFor[BorrowedByteFixture]()}, Methods: map[string][]string{"BorrowedByteFixture": {"Read"}}}
	schema, err := New("borrow", files, opt)
	if err != nil {
		t.Fatal(err)
	}
	method := schema.Package().Methods[0]
	if method.Name != "Read" || method.Ownership == nil || len(method.Ownership.Borrowed) != 1 || method.Ownership.Borrowed[0].Domain != "test.tx" || method.Ownership.Borrowed[0].ScopeParameter != 1 {
		t.Fatal(method)
	}
	gen, err := Generate("borrow", "github.com/andreyvit/evm/evmbind", "bound", files, opt)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(gen, []byte(".View()")) || !bytes.Contains(gen, []byte("f.SetBorrowedBytesResult(0, r0)")) {
		t.Fatal(string(gen))
	}
	files["api.go"].Data = bytes.ReplaceAll([]byte(source), []byte("//evm:borrow-bytes test.tx View"), nil)
	plain, err := New("borrow", files, opt)
	if err != nil {
		t.Fatal(err)
	}
	if plain.src.fingerprint == schema.src.fingerprint {
		t.Fatal("generated wrappers did not invalidate")
	}
	files["api.go"].Data = bytes.ReplaceAll([]byte(source), []byte("Read() []byte"), []byte("Read() int"))
	if _, err := New("borrow", files, opt); err == nil {
		t.Fatal("accepted borrowed scalar result")
	}
}
