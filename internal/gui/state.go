package gui

import (
	"context"
	"fmt"
	"m4s-converter/common"
	"path/filepath"
	"strings"
	"sync"
)

// Options 保存图形界面中与命令行参数一一对应的设置。
type Options struct {
	CachePath  string `json:"cachePath"`  // -c / --cachepath
	OutputDir  string `json:"outputDir"`  // 图形界面新增，空值时保持命令行的缓存目录下 output 行为
	GPACPath   string `json:"gpacPath"`   // -g / --gpacpath
	AssEnabled bool   `json:"assEnabled"` // 对应 !AssOFF，-a / --assoff
	Overlay    bool   `json:"overlay"`    // -o / --overlay
	Summarize  bool   `json:"summarize"`  // -u / --summarize
	GroupByUP  bool   `json:"groupByUP"`  // 图形界面新增：按 UP 主归类输出目录
	OpenOnDone bool   `json:"openOnDone"` // 图形界面行为，不传给核心逻辑
}

// Config 把界面设置转换成核心转换配置，复用原有 Synthesis 流程。
func (o Options) Config(ctx context.Context, observer func(common.ConversionEvent)) *common.Config {
	c := &common.Config{
		CachePath: o.CachePath,
		OutputDir: o.OutputDir,
		GPACPath:  o.GPACPath,
		AssOFF:    !o.AssEnabled,
		Overlay:   o.Overlay,
		Summarize: o.Summarize,
		GroupByUP: o.GroupByUP,
		NoWait:    true,
		Observer:  observer,
	}
	if ctx != nil {
		c.SetContext(ctx)
	}
	return c
}

// TaskRow 是任务列表中的一行。字段名同时作为表格列的数据成员。
type TaskRow struct {
	Group    string `json:"group"`
	Part     string `json:"part"`
	Status   string `json:"status"`
	Progress string `json:"progress"`
	Output   string `json:"output"`
	Message  string `json:"message"`
}

type taskStore struct {
	mu   sync.RWMutex
	rows []*TaskRow
	byID map[string]int
}

func newTaskStore() *taskStore {
	return &taskStore{byID: make(map[string]int)}
}

func (s *taskStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = nil
	s.byID = make(map[string]int)
}

func (s *taskStore) Apply(event common.ConversionEvent) (int, bool) {
	key := event.CacheDir
	if key == "" {
		key = event.OutputFile
	}
	status := string(event.Status)
	if status == "" {
		status = string(common.StatusPreparing)
	}
	progress := event.Progress
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	index, exists := s.byID[key]
	if !exists {
		index = len(s.rows)
		s.byID[key] = index
		s.rows = append(s.rows, &TaskRow{
			Group:    valueOr(event.Group, "未分组"),
			Part:     s.partName(event),
			Status:   status,
			Progress: fmt.Sprintf("%d%%", progress),
			Output:   event.OutputFile,
			Message:  event.Err,
		})
		return index, true
	}

	row := s.rows[index]
	// 每个分P用缓存目录作为稳定标识；只更新自己的状态，不影响其他分P。
	if event.Group != "" {
		row.Group = event.Group
	}
	if event.Part != "" {
		row.Part = event.Part
	}
	if event.OutputFile != "" {
		row.Output = event.OutputFile
	}
	if statusRank(common.ConversionStatus(status)) >= statusRank(common.ConversionStatus(row.Status)) {
		row.Status = status
		row.Progress = fmt.Sprintf("%d%%", progress)
	}
	if event.Err != "" || common.ConversionStatus(status) != common.StatusRunning {
		row.Message = event.Err
	}
	return index, false
}

func (s *taskStore) partName(event common.ConversionEvent) string {
	if event.Part != "" {
		return event.Part
	}
	if event.OutputFile != "" {
		return strings.TrimSuffix(filepath.Base(event.OutputFile), filepath.Ext(event.OutputFile))
	}
	return "未命名分P"
}

func statusRank(status common.ConversionStatus) int {
	switch status {
	case common.StatusPreparing:
		return 1
	case common.StatusRunning:
		return 2
	case common.StatusCompleted, common.StatusSkipped, common.StatusFailed, common.StatusCancelled:
		return 3
	default:
		return 0
	}
}

func (s *taskStore) Snapshot() []*TaskRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := make([]*TaskRow, 0, len(s.rows))
	for _, row := range s.rows {
		copyRow := *row
		rows = append(rows, &copyRow)
	}
	return rows
}

type taskCounts struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
	Running   int `json:"running"`
}

func (s *taskStore) Counts() taskCounts {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var counts taskCounts
	counts.Total = len(s.rows)
	for _, row := range s.rows {
		switch common.ConversionStatus(row.Status) {
		case common.StatusCompleted:
			counts.Completed++
		case common.StatusSkipped:
			counts.Skipped++
		case common.StatusFailed:
			counts.Failed++
		case common.StatusCancelled:
			counts.Cancelled++
		default:
			counts.Running++
		}
	}
	return counts
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
