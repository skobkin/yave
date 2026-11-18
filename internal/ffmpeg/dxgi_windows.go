//go:build windows

package ffmpeg

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"git.skobk.in/skobkin/yave/internal/model"
)

// Raw DXGI adapter enumeration. This file contains only the COM
// binding; the enumeration loop and its completeness contract live in
// dxgi_enum.go (collectAdapters) so they stay testable on Linux.
//
// COM object pointers are carried as unsafe.Pointer from the moment
// the factory call writes them and are only turned into uintptrs at
// call sites — the syscall-approved direction — so the garbage
// collector keeps the objects alive and vet-clean throughout.

var (
	dxgiDLL                = syscall.NewLazyDLL("dxgi.dll")
	procCreateDXGIFactory1 = dxgiDLL.NewProc("CreateDXGIFactory1")
)

var iidIDXGIFactory1 = windows.GUID{
	Data1: 0x770aae78,
	Data2: 0xf26f,
	Data3: 0x4dba,
	Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87},
}

// dxgiErrorNotFound is DXGI_ERROR_NOT_FOUND, the normal end of an
// adapter enumeration.
const dxgiErrorNotFound uintptr = 0x887A0002

// vtable slot maps (MIDL emits methods in declaration order).
//
//	IDXGIFactory (dxgi.h):                      IDXGIAdapter:
//	  0-2  IUnknown (QI, AddRef, Release)         0-2  IUnknown
//	  3    SetPrivateData                         3-6  IDXGIObject
//	  4    SetPrivateDataInterface                7    EnumOutputs
//	  5    GetPrivateData                         8    GetDesc
//	  6    GetParent                              9    CheckInterfaceSupport
//	  7    EnumAdapters        <- used
//	  8    MakeWindowAssociation
//	  9    GetWindowAssociation
//	  10   CreateSwapChain
//	  11   CreateSoftwareAdapter
//
// The struct field order below IS the slot order; Go places fields
// back-to-back, so each named field is the ABI slot. ffmpeg's own
// win32 code calls this exact pair (CreateDXGIFactory + EnumAdapters
// + GetDesc) in hwcontext_vaapi.c / hwcontext_d3d11va.c.
type dxgiFactoryVtbl struct {
	queryInterface, addRef, release uintptr
	setPrivateData, setPrivateDataInterface,
	getPrivateData, getParent uintptr
	enumAdapters uintptr // slot 7
	makeWindowAssociation, getWindowAssociation,
	createSwapChain, createSoftwareAdapter uintptr
}

type dxgiFactory struct {
	vtbl *dxgiFactoryVtbl
}

type dxgiAdapterVtbl struct {
	queryInterface, addRef, release uintptr
	setPrivateData, setPrivateDataInterface,
	getPrivateData, getParent uintptr
	enumOutputs           uintptr // slot 7
	getDesc               uintptr // slot 8
	checkInterfaceSupport uintptr // slot 9
}

type dxgiAdapter struct {
	vtbl *dxgiAdapterVtbl
}

// dxgiAdapterDesc mirrors DXGI_ADAPTER_DESC (dxgi.h field order; the
// Go types must match the C layout exactly, including SIZE_T being
// pointer-sized).
type dxgiAdapterDesc struct {
	description           [128]uint16
	vendorID              uint32
	deviceID              uint32
	subSysID              uint32
	revision              uint32
	dedicatedVideoMemory  uintptr
	dedicatedSystemMemory uintptr
	sharedSystemMemory    uintptr
	adapterLUID           windows.LUID
}

// osGPUAdapterProbe enumerates the DXGI adapters in factory order.
// Any failure degrades to (nil, false); see collectAdapters for the
// completeness contract.
var osGPUAdapterProbe = func() ([]model.GPUAdapter, bool) {
	return enumerateDXGIAdapters()
}

func enumerateDXGIAdapters() ([]model.GPUAdapter, bool) {
	if err := dxgiDLL.Load(); err != nil {
		return nil, false
	}
	var factory unsafe.Pointer
	hr, _, _ := procCreateDXGIFactory1.Call(
		uintptr(unsafe.Pointer(&iidIDXGIFactory1)),
		uintptr(unsafe.Pointer(&factory)),
	)
	if hr != 0 || factory == nil {
		return nil, false
	}
	f := (*dxgiFactory)(factory)
	release := f.vtbl.release
	defer syscall.SyscallN(release, uintptr(unsafe.Pointer(f)))

	// i persists across collectAdapters' calls: EnumAdapters is
	// 0-based and each invocation asks for the next index.
	i := 0
	return collectAdapters(func() (model.GPUAdapter, error) {
		return nextDXGIAdapter(f, &i)
	})
}

// nextDXGIAdapter fetches adapter *i through the factory's
// EnumAdapters slot and reads its description through the adapter's
// GetDesc slot, releasing the adapter before returning. It maps
// DXGI_ERROR_NOT_FOUND to errDXGINotFound and every other failure to
// a plain error.
func nextDXGIAdapter(f *dxgiFactory, i *int) (model.GPUAdapter, error) {
	index := *i
	var adapter unsafe.Pointer
	hr, _, _ := syscall.SyscallN(f.vtbl.enumAdapters,
		uintptr(unsafe.Pointer(f)), uintptr(index), uintptr(unsafe.Pointer(&adapter)))
	if hr == dxgiErrorNotFound {
		return model.GPUAdapter{}, errDXGINotFound
	}
	if hr != 0 || adapter == nil {
		return model.GPUAdapter{}, fmt.Errorf("EnumAdapters(%d): HRESULT 0x%08x", index, hr)
	}
	*i = index + 1

	a := (*dxgiAdapter)(adapter)
	defer syscall.SyscallN(a.vtbl.release, uintptr(unsafe.Pointer(a)))

	var desc dxgiAdapterDesc
	hr, _, _ = syscall.SyscallN(a.vtbl.getDesc,
		uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(&desc)))
	if hr != 0 {
		return model.GPUAdapter{}, fmt.Errorf("GetDesc(%d): HRESULT 0x%08x", index, hr)
	}
	return model.GPUAdapter{
		Index:    index,
		Name:     windows.UTF16ToString(desc.description[:]),
		VendorID: desc.vendorID,
	}, nil
}
