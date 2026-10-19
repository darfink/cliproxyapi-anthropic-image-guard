// Command anthropic-image-guard builds a CLIProxyAPI native interceptor plugin.
package main

/*
#include <stdint.h>
#include <stdlib.h>
typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*free_fn)(void*, size_t);
typedef struct { uint32_t abi_version; void* host_ctx; host_call_fn call; free_fn free_buffer; } cliproxy_host_api;
typedef int (*plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*shutdown_fn)(void);
typedef struct { uint32_t abi_version; plugin_call_fn call; free_fn free_buffer; shutdown_fn shutdown; } cliproxy_plugin_api;
extern int imageGuardCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void imageGuardFree(void*, size_t);
extern void imageGuardShutdown(void);
*/
import "C"

import (
	"unsafe"

	"github.com/darfink/cliproxyapi-anthropic-image-guard/internal/plugin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

var manager = plugin.New()

func main() {}

// The host owns this buffer and keeps it valid until imageGuardCall returns.
// Handle only reads it and JSON decoding copies retained values. Borrowing the
// bytes avoids another full RPC-envelope copy and the C.int size restriction.
func borrowRequest(ptr unsafe.Pointer, size uint64) ([]byte, bool) {
	if size > uint64(^uint(0)>>1) || (size > 0 && ptr == nil) {
		return nil, false
	}
	if size == 0 {
		return nil, true
	}
	return unsafe.Slice((*byte)(ptr), int(size)), true
}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, api *C.cliproxy_plugin_api) C.int {
	if host == nil || api == nil || host.abi_version != C.uint32_t(pluginabi.ABIVersion) {
		return 1
	}
	api.abi_version = C.uint32_t(pluginabi.ABIVersion)
	api.call = C.plugin_call_fn(C.imageGuardCall)
	api.free_buffer = C.free_fn(C.imageGuardFree)
	api.shutdown = C.shutdown_fn(C.imageGuardShutdown)
	return 0
}

//export imageGuardCall
func imageGuardCall(method *C.char, request *C.uint8_t, size C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil {
		return 1
	}
	response.ptr, response.len = nil, 0
	if method == nil {
		return 1
	}
	raw, ok := borrowRequest(unsafe.Pointer(request), uint64(size))
	if !ok {
		return 1
	}
	out := manager.Handle(C.GoString(method), raw)
	if len(out) > 0 {
		response.ptr = C.CBytes(out)
		response.len = C.size_t(len(out))
	}
	return 0
}

//export imageGuardFree
func imageGuardFree(ptr unsafe.Pointer, _ C.size_t) { C.free(ptr) }

//export imageGuardShutdown
func imageGuardShutdown() { manager.Handle(pluginabi.MethodPluginShutdown, nil) }
