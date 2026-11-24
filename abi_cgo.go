//go:build cgo

package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct cliproxy_buffer { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct cliproxy_host_api {
    uint32_t abi_version;
    void* host_ctx;
    cliproxy_host_call_fn call;
    cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct cliproxy_plugin_api {
    uint32_t abi_version;
    cliproxy_plugin_call_fn call;
    cliproxy_plugin_free_fn free_buffer;
    cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;
extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
static cliproxy_host_api stored_host;
static void store_host(const cliproxy_host_api* host) { stored_host = *host; }
static int call_host(const char* method, const uint8_t* request, size_t len, cliproxy_buffer* response) {
    if (stored_host.call == NULL) return 1;
    return stored_host.call(stored_host.host_ctx, method, request, len, response);
}
static void free_host(void* ptr, size_t len) {
    if (ptr != NULL && stored_host.free_buffer != NULL) stored_host.free_buffer(ptr, len);
}
*/
import "C"

import (
	"errors"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil || host.call == nil || host.free_buffer == nil || uint32(host.abi_version) != pluginabi.ABIVersion {
		return 1
	}
	C.store_host(host)
	nativePlugin = NewPlugin(callNativeHost)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) (ret C.int) {
	if response == nil {
		return 1
	}
	response.ptr, response.len = nil, 0
	defer func() {
		if recover() != nil {
			raw, _ := failure("plugin_panic", "tool-emulation native call failed", 500)
			writeResponse(response, raw)
			ret = 1
		}
	}()
	if method == nil || uint64(requestLen) > 2147483647 || (request == nil && requestLen > 0) {
		return 1
	}
	var data []byte
	if requestLen > 0 {
		data = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errCall := nativePlugin.Handle(C.GoString(method), data)
	if errCall != nil {
		raw, _ = failure("plugin_error", "tool-emulation native call failed", 500)
		writeResponse(response, raw)
		return 1
	}
	writeResponse(response, raw)
	return 0
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if len(raw) == 0 {
		return
	}
	response.ptr = C.CBytes(raw)
	if response.ptr != nil {
		response.len = C.size_t(len(raw))
	}
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	_, _ = nativePlugin.Handle(pluginabi.MethodPluginShutdown, []byte(`{}`))
}

func callNativeHost(method string, request []byte) ([]byte, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var cRequest unsafe.Pointer
	if len(request) > 0 {
		cRequest = C.CBytes(request)
		defer C.free(cRequest)
	}
	var response C.cliproxy_buffer
	ret := C.call_host(cMethod, (*C.uint8_t)(cRequest), C.size_t(len(request)), &response)
	defer C.free_host(response.ptr, response.len)
	if uint64(response.len) > 2147483647 {
		return nil, errors.New("host response is too large")
	}
	if response.ptr == nil || response.len == 0 {
		if ret != 0 {
			return nil, errors.New("host callback failed without an envelope")
		}
		return nil, errors.New("host callback returned an empty envelope")
	}
	return C.GoBytes(response.ptr, C.int(response.len)), nil
}
