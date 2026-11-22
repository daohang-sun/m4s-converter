package common

import (
	"bytes"
	"context"
	"fmt"
	"m4s-converter/conver"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bitly/go-simplejson"
)

func TestMP4BoxHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MP4BOX_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "-info" && i+1 < len(os.Args) {
			// 模拟 MP4Box 读取合成时写入的元数据标签。
			data, err := os.ReadFile(os.Args[i+1])
			if err != nil {
				os.Exit(4)
			}
			fmt.Print(string(data))
			os.Exit(0)
		}
	}
	if os.Getenv("MP4BOX_HELPER_BLOCK") == "1" {
		time.Sleep(30 * time.Second)
		os.Exit(1)
	}
	// 从 -tags 中取出 title/artist/album/comment，作为文件内容保存，
	// 供后续 -info 读取，模拟真实 MP4 的元数据存储。
	tags := ""
	for i, arg := range os.Args {
		if arg == "-tags" && i+1 < len(os.Args) {
			// -tags 形如 title=x:artist=y:album=z:comment=w，转成 MP4Box -info 的 "key: value" 行格式。
			parts := strings.Split(os.Args[i+1], ":")
			for _, p := range parts {
				if kv := strings.SplitN(p, "=", 2); len(kv) == 2 {
					tags += kv[0] + ": " + kv[1] + "\n"
				}
			}
			break
		}
	}
	for i, arg := range os.Args {
		if arg == "-new" && i+1 < len(os.Args) {
			if err := os.WriteFile(os.Args[i+1], []byte(tags), 0644); err != nil {
				os.Exit(2)
			}
			os.Exit(0)
		}
	}
	os.Exit(3)
}

func useFakeMP4Box(t *testing.T) {
	t.Helper()
	original := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=TestMP4BoxHelperProcess", "--"}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], helperArgs...)
		cmd.Env = append(os.Environ(), "GO_WANT_MP4BOX_HELPER=1")
		return cmd
	}
	t.Cleanup(func() { execCommandContext = original })
}

func writeTestPart(t *testing.T, root, dir, part string, cid, page int, payload string) {
	t.Helper()
	partDir := filepath.Join(root, dir)
	if err := os.MkdirAll(partDir, 0755); err != nil {
		t.Fatal(err)
	}
	// 不同分P故意使用完全相同的输入字节，验证哈希不会跨分P误判。
	if err := os.WriteFile(filepath.Join(partDir, "video.m4s"), []byte(payload), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partDir, "audio.m4s"), []byte(payload+"-audio"), 0644); err != nil {
		t.Fatal(err)
	}
	// Android 风格缓存：入口文件在缓存根目录，供 GetVAId 区分音视频分片。
	if err := os.WriteFile(filepath.Join(root, conver.PlayEntryJson), []byte(`{"page_data":{"download_title":"completed"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	info := fmt.Sprintf(`{"groupTitle":"系列","uname":"UP","title":"同一视频","itemId":123,"groupId":"group","uid":"user","status":"completed","page_data":{"part":%q,"cid":%d,"page":%d}}`, part, cid, page)
	if err := os.WriteFile(filepath.Join(partDir, "videoInfo.json"), []byte(info), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSynthesisConvertsEveryPartAndResumes(t *testing.T) {
	useFakeMP4Box(t)
	cacheDir := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "custom-output")
	writeTestPart(t, cacheDir, "p1", "第一集", 1001, 1, "same-video")
	writeTestPart(t, cacheDir, "p2", "第二集", 1002, 2, "same-video")
	writeTestPart(t, cacheDir, "p3", "第三集", 1003, 3, "same-video")

	var events []ConversionEvent
	c := Config{
		CachePath: cacheDir,
		OutputDir: outputDir,
		GPACPath:  "fake-mp4box",
		AssOFF:    true,
		NoWait:    true,
		Observer:  func(event ConversionEvent) { events = append(events, event) },
	}
	c.Synthesis()

	// 缓存目录不应产生任何 -video.mp4 / -audio.mp3 中间文件。
	if err := filepath.WalkDir(cacheDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(entry.Name(), conver.VideoSuffix) || strings.HasSuffix(entry.Name(), conver.AudioSuffix) {
			t.Errorf("缓存目录出现中间文件: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	groupDir := filepath.Join(outputDir, "系列-UP")
	for _, part := range []string{"第一集", "第二集", "第三集"} {
		output := filepath.Join(groupDir, part+".mp4")
		if _, err := os.Stat(output); err != nil {
			t.Fatalf("分P %s 未转换: %v", part, err)
		}
		if _, err := os.Stat(stringsTrimSuffix(output) + ".hash"); !os.IsNotExist(err) {
			t.Fatalf("不应再生成 .hash 旁文件: %s", output)
		}
	}

	completed := 0
	for _, event := range events {
		if event.Status == StatusCompleted {
			completed++
		}
	}
	if completed != 3 {
		t.Fatalf("完成事件数=%d，期望3；事件=%+v", completed, events)
	}

	// 第二次运行模拟断点续转：三个输出均应保留并跳过，而不是重新合成。
	var resumedEvents []ConversionEvent
	resumed := Config{CachePath: cacheDir, OutputDir: outputDir, GPACPath: "fake-mp4box", AssOFF: true, NoWait: true,
		Observer: func(event ConversionEvent) { resumedEvents = append(resumedEvents, event) }}
	resumed.Synthesis()
	skipped := 0
	for _, event := range resumedEvents {
		if event.Status == StatusSkipped {
			skipped++
		}
	}
	if skipped != 3 {
		t.Fatalf("断点续转跳过数=%d，期望3；事件=%+v", skipped, resumedEvents)
	}
}

func TestSynthesisAssignsUniqueNamesToSameNamedParts(t *testing.T) {
	useFakeMP4Box(t)
	cacheDir := t.TempDir()
	outputDir := filepath.Join(t.TempDir(), "custom-output")
	// 三个分P使用同一个标题、完全相同的输入字节，验证不再按同名跳过。
	writeTestPart(t, cacheDir, "p1", "同一集", 1001, 1, "same-video")
	writeTestPart(t, cacheDir, "p2", "同一集", 1002, 2, "same-video")
	writeTestPart(t, cacheDir, "p3", "同一集", 1003, 3, "same-video")

	c := Config{CachePath: cacheDir, OutputDir: outputDir, GPACPath: "fake-mp4box", AssOFF: true, NoWait: true}
	c.Synthesis()

	groupDir := filepath.Join(outputDir, "系列-UP")
	for _, name := range []string{"同一集", "同一集_1", "同一集_2"} {
		output := filepath.Join(groupDir, name+".mp4")
		if _, err := os.Stat(output); err != nil {
			t.Fatalf("同名分P %s 未单独输出: %v", name, err)
		}
		if _, err := os.Stat(stringsTrimSuffix(output) + ".hash"); !os.IsNotExist(err) {
			t.Fatalf("不应再生成 .hash 旁文件: %s", output)
		}
	}

	// 第二次运行应各自认领自己的文件并跳过，而不是继续追加 _3。
	var resumedEvents []ConversionEvent
	resumed := Config{CachePath: cacheDir, OutputDir: outputDir, GPACPath: "fake-mp4box", AssOFF: true, NoWait: true,
		Observer: func(event ConversionEvent) { resumedEvents = append(resumedEvents, event) }}
	resumed.Synthesis()
	skipped := 0
	for _, event := range resumedEvents {
		if event.Status == StatusSkipped {
			skipped++
		}
	}
	if skipped != 3 {
		t.Fatalf("断点续转跳过数=%d，期望3；事件=%+v", skipped, resumedEvents)
	}
	if _, err := os.Stat(filepath.Join(groupDir, "同一集_3.mp4")); err == nil {
		t.Fatal("断点续转不应再生成新的后缀文件")
	}
}

func stringsTrimSuffix(path string) string {
	return path[:len(path)-len(".mp4")]
}

// writeMetaMp4 生成一个"仅含元数据"的假 MP4 文件，供判重/认领逻辑读取。
func writeMetaMp4(t *testing.T, path, group, uid, item, comment string) {
	t.Helper()
	content := fmt.Sprintf("title: %s\nartist: %s\nalbum: %s\ncomment: %s\n", group, uid, item, comment)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestIdenticalDetectionIsScopedToPart(t *testing.T) {
	useFakeMP4Box(t)
	dir := t.TempDir()
	video := filepath.Join(dir, "input-video.mp4")
	audio := filepath.Join(dir, "input-audio.mp3")
	_ = os.WriteFile(video, []byte("video-data"), 0644)
	_ = os.WriteFile(audio, []byte("audio-data"), 0644)

	c := Config{PartId: "cid-1001", GroupId: "group", Uid: "user", ItemId: "123"}
	writeMetaMp4(t, filepath.Join(dir, "renamed.mp4"), "group", "user", "123", "cid-1001")

	// 同一分P（元数据 comment 相同）应识别为已合并。
	if exists, _ := c.isIdenticalFileExists(dir, video, audio, "第一集"); !exists {
		t.Fatal("同一分P的已合并文件应被识别")
	}
	// 不同分P（comment 不同）即使其他元数据相同，也不能误判。
	c.PartId = "cid-1002"
	if exists, existing := c.isIdenticalFileExists(dir, video, audio, "第二集"); exists {
		t.Fatalf("不同分P不能判重，误命中: %s", existing)
	}
}

func TestLegacyMp4RequiresExactPartFilename(t *testing.T) {
	useFakeMP4Box(t)
	dir := t.TempDir()
	video := filepath.Join(dir, "v-video.mp4")
	audio := filepath.Join(dir, "a-audio.mp3")
	_ = os.WriteFile(video, []byte("legacy-video"), 0644)
	_ = os.WriteFile(audio, []byte("legacy-audio"), 0644)
	c := Config{PartId: "cid-1", GroupId: "group", Uid: "user", ItemId: "123"}

	// 无 comment 的旧版 MP4：仅精确同名才算已合并。
	writeMetaMp4(t, filepath.Join(dir, "P1.mp4"), "group", "user", "123", "")

	if exists, _ := c.isIdenticalFileExists(dir, video, audio, "P1"); !exists {
		t.Fatal("旧版同名 MP4 仍应支持断点续转")
	}
	if exists, _ := c.isIdenticalFileExists(dir, video, audio, "P10"); exists {
		t.Fatal("P1 不能使用模糊 Contains 匹配 P10")
	}
}

func TestUnreadableMetadataDoesNotSkipConversion(t *testing.T) {
	useFakeMP4Box(t)
	dir := t.TempDir()
	video := filepath.Join(dir, "v-video.mp4")
	_ = os.WriteFile(video, []byte("video"), 0644)
	// 内容为非法 UTF-8 且不含元数据键，parseMP4Metadata 得到空 map，
	// 不能仅凭文件存在就判重跳过。
	if err := os.WriteFile(filepath.Join(dir, "similar.mp4"), []byte("no metadata here"), 0644); err != nil {
		t.Fatal(err)
	}
	c := Config{PartId: "cid-1", GroupId: "group", Uid: "user", ItemId: "123"}
	if exists, _ := c.isIdenticalFileExists(dir, video, filepath.Join(dir, "missing-audio.mp3"), "similar"); exists {
		t.Fatal("元数据不匹配时不能跳过转换")
	}
}

func TestPartIdentityAndMetadataParsing(t *testing.T) {
	js, err := simplejson.NewJson([]byte(`{"page_data":{"cid":1002,"page":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := partIdentity(js, filepath.Join("cache", "fallback")); got != "cid-1002" {
		t.Fatalf("partIdentity=%q", got)
	}
	metadata := parseMP4Metadata("Title: group\r\nArtist: user\nAlbum: 123\nComment: cid-1002")
	if metadata["comment"] != "cid-1002" || metadata["album"] != "123" {
		t.Fatalf("元数据解析失败: %+v", metadata)
	}
}

func TestPrepareM4sInputOnlyStripsLegacyPadding(t *testing.T) {
	dir := t.TempDir()
	content := []byte("0123456789-ftyp-body")

	normal := filepath.Join(dir, "normal.m4s")
	if err := os.WriteFile(normal, content, 0644); err != nil {
		t.Fatal(err)
	}
	got, temp, err := prepareM4sInput(normal)
	if err != nil || temp || got != normal {
		t.Fatalf("新式缓存应直接读取原文件: path=%q temp=%v err=%v", got, temp, err)
	}

	legacy := filepath.Join(dir, "legacy.m4s")
	if err := os.WriteFile(legacy, append([]byte(m4sPadding), content...), 0644); err != nil {
		t.Fatal(err)
	}
	got, temp, err = prepareM4sInput(legacy)
	if err != nil || !temp {
		t.Fatalf("旧式缓存应生成剥离副本: path=%q temp=%v err=%v", got, temp, err)
	}
	defer os.Remove(got)
	data, err := os.ReadFile(got)
	if err != nil || !bytes.Equal(data, content) {
		t.Fatalf("剥离副本内容不正确: %q err=%v", data, err)
	}
}

func TestCompositionCancellationRemovesPartialOutput(t *testing.T) {
	useFakeMP4Box(t)
	t.Setenv("MP4BOX_HELPER_BLOCK", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	c := Config{GPACPath: "fake-mp4box", PartId: "cid-1"}
	c.SetContext(ctx)
	output := filepath.Join(t.TempDir(), "partial.mp4")
	if err := c.Composition("video", "audio", output); err == nil {
		t.Fatal("取消MP4Box后应返回错误")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("取消后不应留下不完整输出: %v", err)
	}
}
