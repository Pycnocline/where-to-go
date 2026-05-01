// Package crashlog 集中处理崩溃日志：把 stderr / log 同时写到文件 + 控制台，
// 并提供 Recover / Wrap 用来拦截 goroutine 内的 panic（写出栈、不让进程整个挂掉）。
//
// Windows GUI 模式下默认没有控制台。Init 会尝试 AttachConsole(ATTACH_PARENT_PROCESS)
// 把当前进程附到启动它的父控制台（cmd / PowerShell）；若没有父控制台则
// AllocConsole 新开一个，让用户能在崩溃后看到栈。日志文件路径固定写到
// `<exe 同级>/where-to-go.crash.log`（追加），不可写时落到 `%TEMP%`。
package crashlog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"time"
)

var (
	initOnce sync.Once
	logFile  *os.File
)

// Init 初始化日志重定向 + 控制台。多次调用安全。返回最终落盘的日志文件路径
// （成功时）或空字符串（写不进去）。
//
// 注意调用顺序：必须先 openLogFile（logFile 句柄供 teeLoop 使用），再
// attachConsole（其中 rebindStdViaPipe 会启动 teeLoop 读 pipe 并写到
// console + logFile）。之后 log.SetOutput(os.Stderr) 让 log 包也走 pipe。
func Init() string {
	var path string
	initOnce.Do(func() {
		path = openLogFile()
		attachConsole()
		// attachConsole 之后 os.Stderr 可能已经是 pipe 写端：写到 os.Stderr
		// 会被 teeLoop 分发到 console + logFile。这里不再用 MultiWriter，
		// 否则会 double-write 到 logFile。
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
		log.Printf("[crashlog] init; pid=%d goos=%s goarch=%s log=%s",
			os.Getpid(), runtime.GOOS, runtime.GOARCH, path)
		log.Printf("[crashlog] runtime fatal panics (如 concurrent map writes) 将经 pipe 写到控制台 + 日志文件")
	})
	return path
}

// Recover 在 defer 里调用，吞掉 panic 并把栈写到日志 + 控制台。
//   defer crashlog.Recover("renderLoop")
//
// 不会重新 panic；调用方若希望在严重 panic 时退出进程，可以显式判断返回值。
func Recover(name string) (recovered bool) {
	if r := recover(); r != nil {
		recovered = true
		stack := debug.Stack()
		out := fmt.Sprintf("\n========== PANIC in %s ==========\n%v\n%s\n========== END PANIC ==========\n",
			name, r, stack)
		// os.Stderr 已被 rebind 到 pipe → tee → console + logFile。
		fmt.Fprint(os.Stderr, out)
		// 强制 sync 一下文件句柄，避免进程很快退出时日志丢失。
		if logFile != nil {
			_ = logFile.Sync()
		}
	}
	return
}

// Wrap 在新 goroutine 里跑 fn，自动 recover；用于把现有的 `go someFn()`
// 升级为 `go crashlog.Wrap("name", someFn)`。
func Wrap(name string, fn func()) {
	defer Recover(name)
	fn()
}

// Sync 立刻把日志落盘（用于已知即将退出前调用）。
func Sync() {
	if logFile != nil {
		_ = logFile.Sync()
	}
}

// HoldThenExit 在严重错误后等 wait 秒（让用户在控制台上看到栈）再退出。
// 主要用在 main 顶层的 defer recover 里。
func HoldThenExit(wait time.Duration, code int) {
	fmt.Fprintf(os.Stderr, "\n[crashlog] holding %s before exit so you can read the log…\n", wait)
	Sync()
	time.Sleep(wait)
	os.Exit(code)
}

func openLogFile() string {
	name := "where-to-go.crash.log"
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), name))
	}
	if d, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(d, name))
	}
	if t := os.TempDir(); t != "" {
		candidates = append(candidates, filepath.Join(t, name))
	}
	for _, p := range candidates {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err == nil {
			fmt.Fprintf(f, "\n----- where-to-go start %s -----\n", time.Now().Format(time.RFC3339))
			logFile = f
			return p
		}
	}
	return ""
}
