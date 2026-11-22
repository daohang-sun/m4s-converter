//go:build !windows

package internal

import "os/exec"

// HideChildConsole 在非 Windows 平台无需处理，保留空实现以保持调用方一致。
func HideChildConsole(cmd *exec.Cmd) {}
