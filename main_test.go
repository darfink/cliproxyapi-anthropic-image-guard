package main

import (
	"bytes"
	"testing"
	"unsafe"
)

func TestBorrowRequest(t *testing.T) {
	if value, ok := borrowRequest(nil, 0); !ok || value != nil {
		t.Fatal("empty request rejected")
	}
	if _, ok := borrowRequest(nil, 1); ok {
		t.Fatal("nil non-empty request accepted")
	}
	raw := []byte(`{"schema_version":2}`)
	ptr := unsafe.Pointer(&raw[0])
	if _, ok := borrowRequest(ptr, uint64(^uint(0)>>1)+1); ok {
		t.Fatal("size_t to int overflow accepted")
	}
	view, ok := borrowRequest(ptr, uint64(len(raw)))
	if !ok || !bytes.Equal(raw, view) || &raw[0] != &view[0] {
		t.Fatal("request was copied or changed")
	}
}
