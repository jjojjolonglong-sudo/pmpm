//go:build windows

// pmpm — 작은 뽀모도로 글자수 창.
// 화면은 index.html(내장), 창은 윈도우에 기본으로 깔린 Edge를 주소창 없는 앱 창으로 띄워 씀.
// 이 exe는 화면 파일을 내보내고, 창 크기·항상 위를 윈도우에 직접 요청하는 역할만 함.
package main

import (
	_ "embed"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

//go:embed index.html
var page []byte

const (
	port  = "47825"
	title = "pmpm 타이머"
)

var (
	user32            = syscall.NewLazyDLL("user32.dll")
	pEnumWindows      = user32.NewProc("EnumWindows")
	pGetClassName     = user32.NewProc("GetClassNameW")
	pGetWindowText    = user32.NewProc("GetWindowTextW")
	pIsWindowVisible  = user32.NewProc("IsWindowVisible")
	pIsWindow         = user32.NewProc("IsWindow")
	pSetWindowPos     = user32.NewProc("SetWindowPos")
	pGetWindowRect    = user32.NewProc("GetWindowRect")
	pGetDpiForWindow  = user32.NewProc("GetDpiForWindow")
	pShowWindow       = user32.NewProc("ShowWindow")
	pSetForeground    = user32.NewProc("SetForegroundWindow")
	pMessageBox       = user32.NewProc("MessageBoxW")
	pGetMonitorInfo   = user32.NewProc("GetMonitorInfoW")
	pMonitorFromWin   = user32.NewProc("MonitorFromWindow")
	pGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")
	pIsIconic         = user32.NewProc("IsIconic")
	pIsZoomed         = user32.NewProc("IsZoomed")
)

// 앱이 정한 창 크기(실제 픽셀). 사용자가 끌어서 바꾸면 이 크기로 되돌림.
var (
	wantMu       sync.Mutex
	wantW, wantH int32
)

type rect struct{ L, T, R, B int32 }
type monitorInfo struct {
	Size          uint32
	Monitor, Work rect
	Flags         uint32
}

var hwnd uintptr

func u16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

var found uintptr

// 콜백은 한 번만 만들어 재사용(윈도우 콜백 개수 제한 때문)
var enumCB = syscall.NewCallback(func(h, _ uintptr) uintptr {
	if v, _, _ := pIsWindowVisible.Call(h); v == 0 {
		return 1
	}
	cls := make([]uint16, 64)
	pGetClassName.Call(h, uintptr(unsafe.Pointer(&cls[0])), 64)
	if !strings.HasPrefix(syscall.UTF16ToString(cls), "Chrome_WidgetWin") {
		return 1
	}
	txt := make([]uint16, 256)
	pGetWindowText.Call(h, uintptr(unsafe.Pointer(&txt[0])), 256)
	if strings.Contains(syscall.UTF16ToString(txt), title) {
		found = h
		return 0
	}
	return 1
})

func findWindow() uintptr {
	found = 0
	pEnumWindows.Call(enumCB, 0)
	return found
}

func alive(h uintptr) bool {
	if h == 0 {
		return false
	}
	v, _, _ := pIsWindow.Call(h)
	return v != 0
}

func win() uintptr {
	if !alive(hwnd) {
		hwnd = findWindow()
	}
	return hwnd
}

func setTop(on bool) {
	h := win()
	if h == 0 {
		return
	}
	after := ^uintptr(1) // HWND_NOTOPMOST (-2)
	if on {
		after = ^uintptr(0) // HWND_TOPMOST (-1)
	}
	const SWP_NOMOVE, SWP_NOSIZE, SWP_NOACTIVATE = 0x2, 0x1, 0x10
	pSetWindowPos.Call(h, after, 0, 0, 0, 0, SWP_NOMOVE|SWP_NOSIZE|SWP_NOACTIVATE)
}

// dw, dh: CSS 픽셀 단위 늘릴 양. 화면 배율을 곱해 실제 픽셀로 바꿈.
func resize(dw, dh int) {
	h := win()
	if h == 0 {
		return
	}
	dpi, _, _ := pGetDpiForWindow.Call(h)
	if dpi == 0 {
		dpi = 96
	}
	s := float64(dpi) / 96
	var r rect
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	w := int32(float64(r.R-r.L) + float64(dw)*s + 0.5*sign(dw))
	ht := int32(float64(r.B-r.T) + float64(dh)*s + 0.5*sign(dh))
	x, y := r.L, r.T
	// 아래로 늘어나다 화면 밖으로 나가면 위로 올려 줌
	mon, _, _ := pMonitorFromWin.Call(h, 2)
	mi := monitorInfo{Size: uint32(unsafe.Sizeof(monitorInfo{}))}
	if mon != 0 {
		if ok, _, _ := pGetMonitorInfo.Call(mon, uintptr(unsafe.Pointer(&mi))); ok != 0 {
			if y+ht > mi.Work.B {
				y = mi.Work.B - ht
			}
			if y < mi.Work.T {
				y = mi.Work.T
			}
		}
	}
	const SWP_NOZORDER, SWP_NOACTIVATE = 0x4, 0x10
	wantMu.Lock()
	pSetWindowPos.Call(h, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(ht), SWP_NOZORDER|SWP_NOACTIVATE)
	var a rect
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&a)))
	wantW, wantH = a.R-a.L, a.B-a.T
	wantMu.Unlock()
}

// 크기 고정: 가장자리 끌기·최대화를 막고, 그래도 바뀌면 되돌림
// 크기 고정: 사용자가 끌어서 바꾸면(마우스를 놓은 뒤) 원래 크기로 되돌림.
// Edge가 스스로 크기를 맞추며 버티면 싸우지 않고 그 크기를 받아들임(떨림 방지).
var lastFix time.Time

func holdSize() {
	h := win()
	if h == 0 {
		return
	}
	if z, _, _ := pIsIconic.Call(h); z != 0 {
		return
	}
	if z, _, _ := pIsZoomed.Call(h); z != 0 {
		pShowWindow.Call(h, 9) // SW_RESTORE
		return
	}
	if b, _, _ := pGetAsyncKeyState.Call(0x01); b&0x8000 != 0 { // 마우스 왼쪽 버튼 누르는 중
		return
	}
	wantMu.Lock()
	defer wantMu.Unlock()
	if wantW == 0 {
		return
	}
	var r rect
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	w, ht := r.R-r.L, r.B-r.T
	if abs32(w-wantW) <= 3 && abs32(ht-wantH) <= 3 {
		return
	}
	if time.Since(lastFix) < 2*time.Second { // 방금 되돌렸는데 또 다르면 Edge 쪽 크기를 인정
		wantW, wantH = w, ht
		return
	}
	lastFix = time.Now()
	const SWP_NOZORDER, SWP_NOACTIVATE = 0x4, 0x10
	pSetWindowPos.Call(h, 0, uintptr(r.L), uintptr(r.T), uintptr(wantW), uintptr(wantH), SWP_NOZORDER|SWP_NOACTIVATE)
}

func abs32(n int32) int32 {
	if n < 0 {
		return -n
	}
	return n
}

func sign(n int) float64 {
	if n < 0 {
		return -1
	}
	return 1
}

func focus() {
	if h := win(); h != 0 {
		pShowWindow.Call(h, 9) // SW_RESTORE
		pSetForeground.Call(h)
	}
}

func alert(msg string) {
	pMessageBox.Call(0, uintptr(unsafe.Pointer(u16(msg))), uintptr(unsafe.Pointer(u16("pmpm"))), 0x30)
}

func browser() string {
	env := func(k string) string { return os.Getenv(k) }
	cands := []string{
		filepath.Join(env("ProgramFiles(x86)"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(env("ProgramFiles"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(env("LOCALAPPDATA"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(env("ProgramFiles"), `Google\Chrome\Application\chrome.exe`),
		filepath.Join(env("ProgramFiles(x86)"), `Google\Chrome\Application\chrome.exe`),
		filepath.Join(env("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// 화면 자동 업데이트: GitHub에 올린 최신 화면을 받아 씀.
// 못 받으면 마지막으로 받은 것, 그것도 없으면 exe에 들어 있는 화면.
const remoteURL = "https://raw.githubusercontent.com/jjojjolonglong-sudo/pmpm/main/index.html"

func cachePath() string { return filepath.Join(os.Getenv("LOCALAPPDATA"), "pmpm", "page.html") }

func valid(b []byte) bool {
	return len(b) > 1000 && len(b) < 4<<20 && strings.Contains(string(b), "<title>pmpm 타이머</title>") && strings.Contains(string(b), "/win/")
}

func latestPage() []byte {
	c := &http.Client{Timeout: 4 * time.Second}
	if resp, err := c.Get(remoteURL + "?t=" + strconv.FormatInt(time.Now().Unix()/60, 10)); err == nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == 200 && valid(b) {
			os.MkdirAll(filepath.Dir(cachePath()), 0o755)
			os.WriteFile(cachePath(), b, 0o644)
			return b
		}
	}
	if b, err := os.ReadFile(cachePath()); err == nil && valid(b) {
		return b
	}
	return page
}

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil { // 이미 켜져 있음 → 그 창을 앞으로
		http.Post("http://127.0.0.1:"+port+"/win/focus", "text/plain", nil)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(latestPage())
	})
	mux.HandleFunc("/win/top", func(w http.ResponseWriter, r *http.Request) {
		setTop(r.URL.Query().Get("on") == "1")
	})
	mux.HandleFunc("/win/resize", func(w http.ResponseWriter, r *http.Request) {
		dw, _ := strconv.Atoi(r.URL.Query().Get("dw"))
		dh, _ := strconv.Atoi(r.URL.Query().Get("dh"))
		if dw > -2000 && dw < 2000 && dh > -2000 && dh < 2000 {
			resize(dw, dh)
		}
	})
	mux.HandleFunc("/win/focus", func(w http.ResponseWriter, r *http.Request) { focus() })
	go http.Serve(ln, mux)

	exe := browser()
	if exe == "" {
		alert("Microsoft Edge(또는 Chrome)를 찾지 못했어요.")
		return
	}
	profile := filepath.Join(os.Getenv("LOCALAPPDATA"), "pmpm", "view")
	cmd := exec.Command(exe,
		"--app=http://127.0.0.1:"+port+"/",
		"--user-data-dir="+profile,
		"--window-size=360,420",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-sync",
		"--autoplay-policy=no-user-gesture-required",
		// 다른 창에 가려지거나 최소화돼도 타이머·소리가 늦어지지 않게
		"--disable-background-timer-throttling",
		"--disable-renderer-backgrounding",
		"--disable-backgrounding-occluded-windows",
		"--disable-features=Translate,msEdgeSidebarV2,msHubApps,msUndersideButton,EdgeCollections,IntensiveWakeUpThrottling,CalculateNativeWinOcclusion",
	)
	if err := cmd.Start(); err != nil {
		alert("창을 여는 데 실패했어요: " + err.Error())
		return
	}
	go cmd.Wait()

	// 창이 뜰 때까지 기다렸다가, 창이 닫히면 함께 종료
	for i := 0; i < 120 && win() == 0; i++ {
		time.Sleep(250 * time.Millisecond)
	}
	if win() == 0 {
		alert("창을 찾지 못했어요. 다시 실행해 주세요.")
		return
	}
	miss := 0
	for {
		time.Sleep(150 * time.Millisecond)
		if win() == 0 {
			miss++
			if miss >= 20 {
				return
			}
			continue
		}
		miss = 0
		holdSize()
	}
}
