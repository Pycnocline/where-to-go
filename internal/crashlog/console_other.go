//go:build !windows

package crashlog

func attachConsole() {
	// 非 Windows：stderr 默认就是控制台，不需要做任何事。
}
