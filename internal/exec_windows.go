//go:build windows

package internal

import (
	"os/exec"
	"syscall"
)

// HideChildConsole 让子进程不弹出控制台窗口。
// GUI 程序使用 -H=windowsgui 编译，调用 MP4Box 时若不加该标志，
// 每个子进程都会闪出一个黑色控制台窗口，影响观感。
func HideChildConsole(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// CREATE_NO_WINDOW = 0x08000000
	cmd.SysProcAttr.CreationFlags |= 0x08000000
}
