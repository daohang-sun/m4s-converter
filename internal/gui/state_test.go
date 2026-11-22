package gui

import (
	"context"
	"m4s-converter/common"
	"path/filepath"
	"testing"
)

func TestTaskStoreTracksPartsIndependently(t *testing.T) {
	store := newTaskStore()
	parts := []struct {
		dir  string
		name string
	}{
		{filepath.Join("cache", "p1"), "第一集"},
		{filepath.Join("cache", "p2"), "第二集"},
		{filepath.Join("cache", "p3"), "第三集"},
	}
	for _, part := range parts {
		store.Apply(common.ConversionEvent{
			CacheDir: part.dir,
			Group:    "同一视频",
			Part:     part.name,
			Status:   common.StatusPreparing,
			Progress: 10,
		})
	}

	rows := store.Snapshot()
	if len(rows) != 3 {
		t.Fatalf("任务行数=%d，期望3", len(rows))
	}
	for i, part := range parts {
		if rows[i].Part != part.name {
			t.Fatalf("第%d行分P=%q，期望%q", i+1, rows[i].Part, part.name)
		}
	}

	store.Apply(common.ConversionEvent{
		CacheDir: parts[0].dir,
		Group:    "同一视频",
		Part:     parts[0].name,
		Status:   common.StatusCompleted,
		Progress: 100,
	})
	rows = store.Snapshot()
	if rows[0].Status != "已完成" || rows[1].Status != "准备中" || rows[2].Status != "准备中" {
		t.Fatalf("更新一个分P影响了其他分P: %+v", rows)
	}
	counts := store.Counts()
	if counts.Total != 3 || counts.Completed != 1 || counts.Running != 2 {
		t.Fatalf("统计错误: %+v", counts)
	}
}

func TestTaskStoreDoesNotDowngradeTerminalStatus(t *testing.T) {
	store := newTaskStore()
	store.Apply(common.ConversionEvent{CacheDir: "p1", Part: "第一集", Status: common.StatusCompleted, Progress: 100})
	store.Apply(common.ConversionEvent{CacheDir: "p1", Part: "第一集", Status: common.StatusPreparing, Progress: 10})
	row := store.Snapshot()[0]
	if row.Status != "已完成" || row.Progress != "100%" {
		t.Fatalf("终态被回退: %+v", row)
	}
}

func TestOptionsConfigMapsOriginalFlags(t *testing.T) {
	var got common.ConversionEvent
	opts := Options{
		CachePath:  "cache",
		OutputDir:  "output",
		GPACPath:   "MP4Box.exe",
		AssEnabled: false,
		Overlay:    true,
		Summarize:  true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := opts.Config(ctx, func(event common.ConversionEvent) { got = event })

	if cfg.CachePath != "cache" || cfg.OutputDir != "output" || cfg.GPACPath != "MP4Box.exe" {
		t.Fatalf("路径映射错误: %+v", cfg)
	}
	if !cfg.AssOFF || !cfg.Overlay || !cfg.Summarize || !cfg.NoWait {
		t.Fatalf("参数映射错误: %+v", cfg)
	}
	cfg.Observer(common.ConversionEvent{Part: "测试"})
	if got.Part != "测试" {
		t.Fatal("事件回调未传递")
	}
}
