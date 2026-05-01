//go:build windows

package crashlog

import (
	"io"
	"os"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// attachConsole 尝试把当前进程挂到父进程的控制台（典型场景：用户在 cmd /
// PowerShell 里敲 `where-to-go.exe`，虽然 exe 是 -H windowsgui 没有自己的
// console，但父 shell 的 console 还在）。若没有父控制台，就 AllocConsole
// 新开一个，用 freopen 把 stdout / stderr / stdin 指过去。
//
// 同时把控制台的输出 code page 切到 UTF-8 (65001)，避免日志中的中文字符串
// 在默认 CP936 下出现乱码。
func attachConsole() {
	const (
		ATTACH_PARENT_PROCESS = ^uintptr(0) // -1 as DWORD
		CP_UTF8               = 65001
	)
	k := windows.NewLazySystemDLL("kernel32.dll")
	procAttach := k.NewProc("AttachConsole")
	procAlloc := k.NewProc("AllocConsole")
	procSetTitle := k.NewProc("SetConsoleTitleW")
	procSetCPOut := k.NewProc("SetConsoleOutputCP")
	procSetCPIn := k.NewProc("SetConsoleCP")

	r, _, _ := procAttach.Call(ATTACH_PARENT_PROCESS)
	if r == 0 {
		// 没有父控制台，开个新窗口。对用户来说会多弹一个黑窗口，但这是
		// 用户明确要求的 "保留命令行存活" 的最简实现。
		procAlloc.Call()
	}
	rebindStdViaPipe()
	// UTF-8 code page：让 log.Printf("加载中...") 里的中文不再乱码。
	procSetCPOut.Call(CP_UTF8)
	procSetCPIn.Call(CP_UTF8)
	title, _ := syscall.UTF16PtrFromString("where-to-go · crash log")
	procSetTitle.Call(uintptr(unsafe.Pointer(title)))
}

// rebindStdViaPipe 把 os.Stdout / os.Stderr 替换成一个 pipe 的写入端，并把
// Win32 STD_OUTPUT_HANDLE / STD_ERROR_HANDLE 也指到 pipe 写入端。然后开
// 一条后台 goroutine 把 pipe 读出端的字节同时分发到：
//   1. 真正的控制台 (CONOUT$)
//   2. 崩溃日志文件 logFile（若已打开）
//
// 这样：
//   - log.Printf / fmt.Fprintln(os.Stderr, ...) → pipe → 控制台 + 文件
//   - Go 运行时 fatal panic（"concurrent map writes" 等）会经 WriteFile 到
//     STD_ERROR_HANDLE = pipe → 同样落到控制台 + 文件，**这是 recover 抓
//     不住的崩溃唯一能在事后看到的方式**。
//
// 一次 init only —— rebind 永久生效，不可逆。
var rebindOnce sync.Once

func rebindStdViaPipe() {
	rebindOnce.Do(func() {
		conout, errOut := os.OpenFile("CONOUT$", os.O_WRONLY, 0)
		if errOut != nil {
			conout = nil
		}
		// stdin 不需要 tee，直接绑到 CONIN$
		if f, err := os.OpenFile("CONIN$", os.O_RDONLY, 0); err == nil {
			os.Stdin = f
		}
		// out / err 共用一个 pipe，避免维护两条链路。
		pr, pw, err := os.Pipe()
		if err != nil {
			// 退化：直接绑到 console（runtime fatal 拿不到，但至少 log 能看到）
			if conout != nil {
				os.Stdout = conout
				os.Stderr = conout
			}
			return
		}
		os.Stdout = pw
		os.Stderr = pw
		// 把 OS 层面的 STD handle 也指过去，runtime panic 才会进 pipe
		_ = windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(pw.Fd()))
		_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(pw.Fd()))
		// reader goroutine：read → fanout
		go teeLoop(pr, conout)
	})
}

// teeLoop 从 pipe 读出端阻塞读取，写到 console + logFile。任何写错都忽略，
// 永不退出（直到进程结束）。
func teeLoop(r *os.File, conout *os.File) {
	defer func() {
		// 防御：teeLoop 自己 panic 不要打到 pipe（会无限递归），降级到
		// 直接写文件。
		if rec := recover(); rec != nil {
			if logFile != nil {
				_, _ = logFile.WriteString("\n[crashlog teeLoop panic]\n")
			}
		}
	}()
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			data := buf[:n]
			if conout != nil {
				_, _ = conout.Write(data)
			}
			if logFile != nil {
				_, _ = logFile.Write(data)
				_ = logFile.Sync()
			}
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			return
		}
	}
}

