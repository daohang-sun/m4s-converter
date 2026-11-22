//go:build windows

package internal

import (
	"os/exec"
	"testing"
)

func TestHideChildConsoleSetsNoWindowFlag(t *testing.T) {
	cmd := exec.Command("dummy")
	HideChildConsole(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatalf("未设置 CREATE_NO_WINDOW: %+v", cmd.SysProcAttr)
	}
}
