package common

import (
	"m4s-converter/conver"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildOutputPlanFoldersAndStatuses(t *testing.T) {
	useFakeMP4Box(t)
	cacheDir := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "planned-output")
	writeTestPart(t, cacheDir, "p1", "第一集", 2001, 1, "video")
	writeTestPart(t, cacheDir, "p2", "第二集", 2002, 2, "video")

	plan := BuildOutputPlan(cacheDir, outputDir, false)
	if len(plan.Files) != 2 {
		t.Fatalf("默认规划应有 2 个文件，实际 %d", len(plan.Files))
	}
	for _, file := range plan.Files {
		if file.Folder != "系列-UP" {
			t.Fatalf("默认目录应为 系列-UP，实际 %q", file.Folder)
		}
		if file.Status != "待转换" {
			t.Fatalf("新文件状态应为待转换，实际 %q", file.Status)
		}
	}
	if _, err := os.Stat(outputDir); !os.IsNotExist(err) {
		t.Fatal("预览阶段不应创建输出目录")
	}

	grouped := BuildOutputPlan(cacheDir, outputDir, true)
	if len(grouped.Files) != 2 {
		t.Fatalf("归类规划应有 2 个文件，实际 %d", len(grouped.Files))
	}
	for _, file := range grouped.Files {
		if filepath.ToSlash(file.Folder) != "UP/系列" {
			t.Fatalf("归类目录应为 UP/系列，实际 %q", file.Folder)
		}
	}
	if _, err := os.Stat(outputDir); !os.IsNotExist(err) {
		t.Fatal("归类预览同样不应创建输出目录")
	}
}

func TestBuildOutputPlanHandlesDuplicatesExistingAndIncomplete(t *testing.T) {
	useFakeMP4Box(t)
	cacheDir := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "out")
	writeTestPart(t, cacheDir, "p1", "同一集", 3001, 1, "video")
	writeTestPart(t, cacheDir, "p2", "同一集", 3002, 2, "video")

	plan := BuildOutputPlan(cacheDir, outputDir, false)
	if len(plan.Files) != 2 {
		t.Fatalf("应有 2 个分P，实际 %d", len(plan.Files))
	}
	names := map[string]string{}
	for _, file := range plan.Files {
		names[file.Name] = file.Status
	}
	if names["同一集.mp4"] != "待转换" || names["同一集_1.mp4"] != "待转换" {
		t.Fatalf("同名分P规划应带 _1 后缀，实际 %v", names)
	}

	// 已存在的输出文件应标记为“已存在”，且不应再分配新后缀。
	existingDir := filepath.Join(outputDir, "系列-UP")
	if err := os.MkdirAll(existingDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existingDir, "同一集.mp4"), []byte("done"), 0644); err != nil {
		t.Fatal(err)
	}
	second := BuildOutputPlan(cacheDir, outputDir, false)
	statuses := map[string]string{}
	for _, file := range second.Files {
		statuses[file.Name] = file.Status
	}
	if statuses["同一集.mp4"] != "已存在" {
		t.Fatalf("已有文件应标记为已存在，实际 %v", statuses)
	}
	if _, ok := statuses["同一集_2.mp4"]; ok {
		t.Fatalf("断点续转不应继续追加后缀: %v", statuses)
	}

	// 缓存未完成的分P只预览，不参与命名占用。
	incomplete := filepath.Join(cacheDir, "p3")
	if err := os.MkdirAll(incomplete, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(incomplete, "video.m4s"), []byte("v"), 0644)
	_ = os.WriteFile(filepath.Join(incomplete, "audio.m4s"), []byte("a"), 0644)
	_ = os.WriteFile(filepath.Join(incomplete, conver.VideoInfoJson),
		[]byte(`{"groupTitle":"系列","uname":"UP","title":"未完成","status":"downloading","page_data":{"part":"未完成集","cid":3003,"page":3}}`), 0644)
	third := BuildOutputPlan(cacheDir, outputDir, false)
	found := false
	for _, file := range third.Files {
		if file.Name == "未完成集.mp4" && file.Status == "缓存未完成" {
			found = true
		}
	}
	if !found {
		t.Fatalf("未完成缓存应显示为缓存未完成: %+v", third.Files)
	}
}

func TestSynthesisHonorsGroupByUP(t *testing.T) {
	useFakeMP4Box(t)
	cacheDir := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "grouped-out")
	writeTestPart(t, cacheDir, "p1", "第一集", 4001, 1, "video")

	c := Config{
		CachePath: cacheDir,
		OutputDir: outputDir,
		GPACPath:  "fake-mp4box",
		AssOFF:    true,
		GroupByUP: true,
		NoWait:    true,
	}
	c.Synthesis()

	want := filepath.Join(outputDir, "UP", "系列", "第一集.mp4")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("按 UP 主归类后输出路径不符: %v", err)
	}
}

func TestGroupByUPUsesOwnerNameNotTitle(t *testing.T) {
	// 模拟真实 B 站缓存：groupTitle/uname 为空，owner_name 是 UP 主，title 是视频标题。
	cacheDir := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "real-out")
	partDir := filepath.Join(cacheDir, "p1")
	if err := os.MkdirAll(partDir, 0755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(partDir, "video.m4s"), []byte("v"), 0644)
	_ = os.WriteFile(filepath.Join(partDir, "audio.m4s"), []byte("a"), 0644)
	_ = os.WriteFile(filepath.Join(cacheDir, conver.PlayEntryJson),
		[]byte(`{"page_data":{"download_title":"completed"}}`), 0644)
	info := `{"groupTitle":"","uname":"","owner_name":"正人君子字幕组","title":"和tina学习大红帽","itemId":123,"groupId":"g","uid":"u","status":"completed","page_data":{"part":"和tina学习大红帽","cid":5001,"page":1}}`
	_ = os.WriteFile(filepath.Join(partDir, conver.VideoInfoJson), []byte(info), 0644)

	plan := BuildOutputPlan(cacheDir, outputDir, true)
	if len(plan.Files) != 1 {
		t.Fatalf("应有 1 个文件，实际 %d", len(plan.Files))
	}
	want := filepath.Join("正人君子字幕组", "和tina学习大红帽", "和tina学习大红帽.mp4")
	if filepath.ToSlash(plan.Files[0].Relative) != filepath.ToSlash(want) {
		t.Fatalf("归类目录应取 owner_name 作 UP 主层，期望 %q，实际 %q", want, plan.Files[0].Relative)
	}

	// 默认结构不受影响：仍为 groupTitle(回退 owner_name)-uname(回退 title)。
	plain := BuildOutputPlan(cacheDir, outputDir, false)
	if plain.Files[0].Folder != "正人君子字幕组-和tina学习大红帽" {
		t.Fatalf("默认文件夹名应保持原行为，实际 %q", plain.Files[0].Folder)
	}
}
