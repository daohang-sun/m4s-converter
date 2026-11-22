//go:build windows

package gui

import (
	"os"
	"syscall"
	"unsafe"

	"github.com/lxn/win"
)

const (
	bifReturnOnlyFSDirs = 0x00000001
	bifNewDialogStyle   = 0x00000040 // 新式对话框：可调整大小、带地址栏，体验接近资源管理器
	bffmInitialized     = 1
	bffmSetSelection    = win.WM_USER + 103
)

// browseFolderCallback 在对话框初始化时仅"选中"初始目录，而不把它设为根目录。
// 这是关键区别：旧实现把初始目录设为 PidlRoot，用户无法向上浏览；
// 这里只做初始选中，用户可以像在资源管理器中一样自由浏览上下级。
func browseFolderCallback(hwnd win.HWND, msg uint32, lp, data uintptr) uintptr {
	if msg == bffmInitialized && data != 0 {
		win.SendMessage(hwnd, bffmSetSelection, 1, data)
	}
	return 0
}

var browseFolderCallbackPtr uintptr

func init() {
	browseFolderCallbackPtr = syscall.NewCallback(browseFolderCallback)
}

// chooseDirNative 打开 Windows 原生文件夹选择对话框。
// initial 只作为初始选中目录（可为空），不会限制浏览范围。
func chooseDirNative(title, initial string, owner win.HWND) (string, bool) {
	var selection *uint16
	if initial != "" {
		if info, err := os.Stat(initial); err == nil && info.IsDir() {
			selection = syscall.StringToUTF16Ptr(initial)
		}
	}

	bi := win.BROWSEINFO{
		HwndOwner: owner,
		UlFlags:   bifReturnOnlyFSDirs | bifNewDialogStyle,
		LpszTitle: syscall.StringToUTF16Ptr(title),
		Lpfn:      browseFolderCallbackPtr,
	}
	if selection != nil {
		bi.LParam = uintptr(unsafe.Pointer(selection))
	}

	// BIF_NEWDIALOGSTYLE 需要 COM。walk 的 UI 线程通常已初始化为 STA，
	// 此时 OleInitialize 返回 S_FALSE，不应由这里重复释放。
	hr := win.OleInitialize()
	if hr != win.S_OK && hr != win.S_FALSE {
		return "", false
	}
	initializedHere := hr == win.S_OK

	pidl := win.SHBrowseForFolder(&bi)
	if pidl == 0 {
		if initializedHere {
			win.OleUninitialize()
		}
		return "", false
	}
	defer win.CoTaskMemFree(pidl)
	if initializedHere {
		defer win.OleUninitialize()
	}

	var buf [win.MAX_PATH]uint16
	if !win.SHGetPathFromIDList(pidl, &buf[0]) {
		return "", false
	}
	return syscall.UTF16ToString(buf[:]), true
}
