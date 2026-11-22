//go:build windows

package gui

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"m4s-converter/common"
	"m4s-converter/internal"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	webview2 "github.com/jchv/go-webview2"
	"github.com/lxn/walk"
	"github.com/lxn/win"
	"github.com/sirupsen/logrus"
)

//go:embed modern.html
var modernHTML string

type initialState struct {
	Options Options `json:"options"`
}

type operationResult struct {
	OK      bool    `json:"ok"`
	Message string  `json:"message"`
	Options Options `json:"options"`
}

type taskUpdate struct {
	Rows       []*TaskRow `json:"rows"`
	Counts     taskCounts `json:"counts"`
	Percent    int        `json:"percent"`
	Running    bool       `json:"running"`
	Cancelling bool       `json:"cancelling"`
}

type finishUpdate struct {
	Cancelled bool       `json:"cancelled"`
	Counts    taskCounts `json:"counts"`
	OutputDir string     `json:"outputDir"`
}

type webApp struct {
	w     webview2.WebView
	store *taskStore

	mu         sync.Mutex
	cfg        *common.Config
	cancel     context.CancelFunc
	running    bool
	cancelling bool
	outputDir  string
	openOnDone bool
}

// Run 启动基于 WebView2 的现代界面。HTML、CSS、JavaScript 全部内嵌，无需本地服务器。
func Run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	app := &webApp{store: newTaskStore()}
	dataDir := ""
	if cache, err := os.UserCacheDir(); err == nil {
		dataDir = filepath.Join(cache, "m4s-converter", "webview")
		_ = os.MkdirAll(dataDir, 0755)
	}

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		DataPath:  dataDir,
		WindowOptions: webview2.WindowOptions{
			Title:  "B 站缓存转换",
			Width:  1280,
			Height: 900,
			IconId: 2, // rsrc 生成的图标组资源 ID；ID 1 是应用程序清单。
			Center: true,
		},
	})
	if w == nil {
		startupError("无法初始化 WebView2。请确认系统已安装 Microsoft Edge WebView2 Runtime。")
		return
	}
	app.w = w
	defer w.Destroy()
	defer app.stop()

	bindings := []struct {
		name string
		fn   interface{}
	}{
		{"getInitialState", app.initialState},
		{"pickDirectory", app.pickDirectory},
		{"pickMP4Box", app.pickMP4Box},
		{"buildPreview", app.buildPreview},
		{"startConversion", app.startConversion},
		{"cancelConversion", app.cancelConversion},
		{"openOutput", app.openOutput},
		{"openTask", app.openTask},
	}
	for _, binding := range bindings {
		if err := w.Bind(binding.name, binding.fn); err != nil {
			startupError(fmt.Sprintf("初始化界面功能 %s 失败：%v", binding.name, err))
			return
		}
	}

	w.SetSize(1280, 900, webview2.HintNone)
	w.SetSize(1000, 700, webview2.HintMin)
	logrus.AddHook(&webLogHook{app: app})
	w.SetHtml(modernHTML)
	w.Run()
}

func startupError(message string) {
	errorFile := filepath.Join(filepath.Dir(os.Args[0]), "gui-startup-error.txt")
	_ = os.WriteFile(errorFile, []byte(message), 0644)
	walk.MsgBox(nil, "B 站缓存转换启动失败", message, walk.MsgBoxOK|walk.MsgBoxIconError)
}

func (a *webApp) initialState() (initialState, error) {
	home, _ := os.UserHomeDir()
	cachePath := ""
	if home != "" {
		cachePath = filepath.Join(home, "Videos", "bilibili")
	}
	outputPath := ""
	if info, err := os.Stat(cachePath); cachePath == "" || err != nil || !info.IsDir() {
		cachePath = ""
	} else {
		outputPath = filepath.Join(cachePath, "output")
	}
	return initialState{Options: Options{
		CachePath:  cachePath,
		OutputDir:  outputPath,
		GPACPath:   internal.GetMP4Box(),
		AssEnabled: true,
		OpenOnDone: true,
	}}, nil
}

// pickDirectory 保留 Windows 原生目录选择器，初始目录不会限制可浏览范围。
func (a *webApp) pickDirectory(kind, current string) (string, error) {
	title := "选择 B 站缓存目录"
	if kind == "output" {
		title = "选择输出目录"
	}
	owner := win.HWND(uintptr(a.w.Window()))
	path, ok := chooseDirNative(title, strings.TrimSpace(current), owner)
	if !ok {
		return "", nil
	}
	return path, nil
}

func (a *webApp) pickMP4Box(current string) (string, error) {
	dlg := &walk.FileDialog{
		Title:          "选择 MP4Box.exe",
		Filter:         "可执行文件 (*.exe)|*.exe",
		FilePath:       current,
		InitialDirPath: filepath.Dir(current),
	}
	accepted, err := dlg.ShowOpen(nil)
	if err != nil {
		return "", err
	}
	if !accepted {
		return "", nil
	}
	return dlg.FilePath, nil
}

func (a *webApp) buildPreview(options Options) (common.OutputPlan, error) {
	cache := strings.TrimSpace(options.CachePath)
	if cache == "" {
		return common.OutputPlan{}, fmt.Errorf("请先选择缓存输入目录")
	}
	info, err := os.Stat(cache)
	if err != nil || !info.IsDir() {
		return common.OutputPlan{}, fmt.Errorf("缓存输入目录不存在，请重新选择")
	}
	output := strings.TrimSpace(options.OutputDir)
	if output == "" {
		output = filepath.Join(cache, "output")
	}
	return common.BuildOutputPlan(cache, output, options.GroupByUP), nil
}

func normalizeOptions(options Options) (Options, error) {
	options.CachePath = strings.TrimSpace(options.CachePath)
	options.OutputDir = strings.TrimSpace(options.OutputDir)
	options.GPACPath = strings.TrimSpace(options.GPACPath)
	if options.CachePath == "" {
		return options, fmt.Errorf("请先选择缓存输入目录")
	}
	info, err := os.Stat(options.CachePath)
	if err != nil || !info.IsDir() {
		return options, fmt.Errorf("缓存输入目录不存在：%s", options.CachePath)
	}
	if options.OutputDir == "" {
		options.OutputDir = filepath.Join(options.CachePath, "output")
	}
	if options.GPACPath == "" {
		options.GPACPath = internal.GetMP4Box()
	}
	if info, err = os.Stat(options.GPACPath); err != nil || info.IsDir() {
		return options, fmt.Errorf("找不到 MP4Box，请在高级设置中重新选择")
	}
	return options, nil
}

func (a *webApp) startConversion(options Options) (operationResult, error) {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return operationResult{}, fmt.Errorf("转换任务正在运行")
	}
	a.mu.Unlock()

	normalized, err := normalizeOptions(options)
	if err != nil {
		return operationResult{}, err
	}

	a.mu.Lock()
	a.running = true
	a.cancelling = false
	a.outputDir = normalized.OutputDir
	a.openOnDone = normalized.OpenOnDone
	a.mu.Unlock()
	a.store.Reset()
	a.pushTasks()

	ctx, cancel := context.WithCancel(context.Background())
	cfg := normalized.Config(ctx, func(event common.ConversionEvent) {
		a.store.Apply(event)
		a.pushTasks()
	})
	a.mu.Lock()
	a.cfg = cfg
	a.cancel = cancel
	a.mu.Unlock()

	logrus.Infof("开始转换：缓存目录=%s，输出目录=%s", normalized.CachePath, normalized.OutputDir)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				logrus.Errorf("转换线程发生异常：%v", recovered)
			}
			cancel()
			a.finish()
		}()
		cfg.Synthesis()
	}()

	return operationResult{OK: true, Message: "转换任务已开始", Options: normalized}, nil
}

func (a *webApp) cancelConversion() (bool, error) {
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return false, nil
	}
	a.cancelling = true
	cfg, cancel := a.cfg, a.cancel
	a.mu.Unlock()
	if cfg != nil {
		cfg.SetExitFlag(true)
	}
	if cancel != nil {
		cancel()
	}
	logrus.Warn("用户请求取消转换任务")
	a.pushTasks()
	return true, nil
}

func (a *webApp) finish() {
	a.mu.Lock()
	cancelled := a.cancelling
	outputDir := a.outputDir
	openOnDone := a.openOnDone
	a.running = false
	a.cancelling = false
	a.cfg = nil
	a.cancel = nil
	a.mu.Unlock()

	counts := a.store.Counts()
	a.pushTasks()
	a.emit("onFinish", finishUpdate{Cancelled: cancelled, Counts: counts, OutputDir: outputDir})
	if !cancelled && openOnDone && counts.Failed == 0 && counts.Total > 0 {
		common.OpenFolder(outputDir)
	}
}

func (a *webApp) stop() {
	a.mu.Lock()
	cfg, cancel := a.cfg, a.cancel
	a.running = false
	a.mu.Unlock()
	if cfg != nil {
		cfg.SetExitFlag(true)
	}
	if cancel != nil {
		cancel()
	}
}

func (a *webApp) openOutput(path string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, fmt.Errorf("请先选择输出目录")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false, fmt.Errorf("输出目录尚未生成，请先完成一次转换")
	}
	common.OpenFolder(path)
	return true, nil
}

func (a *webApp) openTask(path string) (bool, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return false, fmt.Errorf("该任务还没有输出文件")
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("输出文件尚不存在")
	}
	if info.IsDir() {
		common.OpenFolder(path)
	} else {
		common.OpenFolder(filepath.Dir(path))
	}
	return true, nil
}

func (a *webApp) pushTasks() {
	rows := a.store.Snapshot()
	counts := a.store.Counts()
	percent := 0
	if len(rows) > 0 {
		sum := 0
		for _, row := range rows {
			value, err := strconv.Atoi(strings.TrimSuffix(row.Progress, "%"))
			if err == nil {
				sum += value
			}
		}
		percent = sum / len(rows)
	}
	a.mu.Lock()
	running, cancelling := a.running, a.cancelling
	a.mu.Unlock()
	a.emit("onTasks", taskUpdate{
		Rows: rows, Counts: counts, Percent: percent,
		Running: running, Cancelling: cancelling,
	})
}

func (a *webApp) emit(method string, payload interface{}) {
	if a.w == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	a.w.Dispatch(func() {
		a.w.Eval("window.desktop && window.desktop." + method + "(" + string(data) + ");")
	})
}

// webLogHook 将核心日志实时发送到界面的日志页签。
type webLogHook struct {
	app *webApp
}

func (h *webLogHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *webLogHook) Fire(entry *logrus.Entry) error {
	h.app.emit("onLog", map[string]string{
		"time":    entry.Time.Format("15:04:05"),
		"level":   strings.ToLower(entry.Level.String()),
		"message": entry.Message,
	})
	return nil
}
