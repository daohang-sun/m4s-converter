package common

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"m4s-converter/conver"
	"m4s-converter/internal"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	utils "github.com/mzky/utils/common"
	"github.com/ncruces/zenity"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type Config struct {
	CachePath  string
	Overlay    bool
	AssPath    string
	AssOFF     bool
	OutputDir  string
	GPACPath   string
	Summarize  bool
	GroupByUP  bool // 按 UP 主归类：输出目录为 output/UP主/合集/分P.mp4
	NoWait     bool
	Observer   func(ConversionEvent)
	video      string
	audio      string
	ItemId     string
	GroupId    string
	Uid        string
	PartId     string
	Title      string
	Uname      string
	GroupTitle string
	ExitFlag   bool

	mu  sync.RWMutex
	ctx context.Context

	// assignedOutputs 记录本次 Synthesis 中每个输出文件归属于哪个分P，
	// 用于同一文件夹下同名分P自动分配 _1/_2/... 后缀。
	assignedOutputs map[string]string

	// skipMetadataCheck 仅供输出预览使用：按候选文件名认领已存在的文件，
	// 不启动 MP4Box 读取元数据，保证预览快速且可预测。
	skipMetadataCheck bool
}

func (c *Config) overlay() string {
	if c.Overlay {
		return "-y"
	}
	return "-n"
}

// SetExitFlag 设置退出标志。
func (c *Config) SetExitFlag(flag bool) {
	c.mu.Lock()
	c.ExitFlag = flag
	c.mu.Unlock()
}

// ShouldExit 检查是否应该退出。
func (c *Config) ShouldExit() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ExitFlag
}

// SetContext 为GUI等调用方设置可取消的执行上下文；CLI默认使用Background。
func (c *Config) SetContext(ctx context.Context) {
	c.mu.Lock()
	c.ctx = ctx
	c.mu.Unlock()
}

func (c *Config) context() context.Context {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

var execCommandContext = exec.CommandContext

func (c *Config) Composition(videoFile, audioFile, outputFile string) error {
	var cmd *exec.Cmd
	// 构建MP4Box命令行参数
	var args []string
	// 添加覆盖参数
	if c.Overlay {
		args = append(args, "-force")
	}

	// 添加字符集参数，指定使用UTF-8编码
	args = append(args, "-charset", "utf8")

	// 同一视频的各个分P共享GroupId/Uid/ItemId，comment用于区分分P身份。
	tags := fmt.Sprintf("title=%s:artist=%s:album=%s:comment=%s", c.GroupId, c.Uid, c.ItemId, c.PartId)
	args = append(args, "-tags", tags)
	args = append(args,
		// "-quiet", // 仅打印异常日志
		"-cprt", c.ItemId,
		"-add", videoFile+"#video",
		"-add", audioFile+"#audio",
		"-new", outputFile)
	cmd = execCommandContext(c.context(), c.GPACPath, args...)
	internal.HideChildConsole(cmd)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	if c.AssPath != "" {
		assFile := strings.ReplaceAll(outputFile, conver.Mp4Suffix, conver.AssSuffix)
		_ = c.copyFile(c.AssPath, assFile)
	}

	// 等待命令执行完成
	if err := cmd.Run(); err != nil {
		if c.context().Err() != nil {
			// CommandContext终止进程后可能留下不完整的输出，不能让它影响断点续转。
			_ = os.Remove(outputFile)
		}
		logrus.Errorf("合成视频文件失败:%s\n%s", outputFile, stdout.String())
		return err
	}

	logrus.Info("已合成视频文件:", outputFile)
	return nil
}

func GetCacheDir(cachePath string) ([]string, error) {
	var dirs []string
	err := filepath.Walk(cachePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && path != cachePath {
			if !strings.Contains(path, "output") {
				dirs = append(dirs, path)
			}
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return dirs, nil
}

func joinUrl(cid string) string {
	return "https://comment.bilibili.com/" + cid + conver.XmlSuffix
}
func joinXmlUrl(cid string) string {
	return "https://api.bilibili.com/x/v1/dm/list.so?oid=" + cid
}

// GetAudioAndVideo 从给定的缓存路径中直接查找视频和音频 m4s 分片。
// 不生成任何中间文件，MP4Box 可以直接读取 m4s 完成合并。
func (c *Config) GetAudioAndVideo(cachePath string) (string, string, error) {
	var video, audio string

	entries, err := os.ReadDir(cachePath)
	if err != nil {
		return "", "", err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// 如果是目录，递归查找
			childVideo, childAudio, err := c.GetAudioAndVideo(filepath.Join(cachePath, entry.Name()))
			if err == nil && childVideo != "" && childAudio != "" {
				video, audio = childVideo, childAudio
				break
			}
			continue
		}

		fileName := entry.Name()
		if !strings.HasSuffix(fileName, conver.M4sSuffix) {
			continue
		}
		fullPath := filepath.Join(cachePath, fileName)
		videoId, audioId := GetVAId(fullPath)
		if videoId == "" || audioId == "" {
			continue
		}
		if strings.Contains(fileName, audioId) {
			audio = fullPath
		} else {
			video = fullPath
		}
	}

	// 如果在当前目录及其子目录中都找不到视频或音频 m4s，返回错误
	if video == "" || audio == "" {
		return "", "", fmt.Errorf("找不到音频或视频 m4s 文件: %s", cachePath)
	}

	// 下载弹幕文件（保持原有行为）
	if !c.AssOFF {
		oldVideo := c.video
		c.video = video
		c.downloadXml()
		c.video = oldVideo
	}
	return video, audio, nil
}
func (c *Config) copyFile(src, dst string) error {
	// 打开源文件
	srcFile, err := os.Open(src)
	if err != nil {
		logrus.Errorf("打开源文件失败: %v", err)
		return err
	}
	defer srcFile.Close()

	// 创建目标文件
	dstFile, err := os.Create(dst)
	if err != nil {
		logrus.Errorf("创建目标文件失败: %v", err)
		return err
	}
	defer dstFile.Close()

	// 读取前 9 个字节
	data := make([]byte, 9)
	if _, err := io.ReadAtLeast(srcFile, data, 9); err != nil {
		logrus.Errorf("读取文件头失败: %v", err)
		return err
	}

	// 检查前 9 个字节是否为 '0'
	if string(data) != "000000000" {
		// 如果前 9 个字节不为 '0'，写入这些字节
		if _, err := dstFile.Write(data); err != nil {
			logrus.Errorf("写入文件头失败: %v", err)
			return err
		}
	}

	// 使用缓冲读取器逐块读取并写入文件
	if _, err := io.Copy(bufio.NewWriter(dstFile), bufio.NewReader(srcFile)); err != nil {
		logrus.Errorf("读取或写入文件失败: %v", err)
		return err
	}
	return nil
}

// m4sPadding 是旧版 B 站缓存 m4s 文件头部的 9 字节填充。
const m4sPadding = "000000000"

// prepareM4sInput 检查 m4s 头部，仅当存在旧式填充时在系统临时目录生成剥离后的副本。
// 返回实际供 MP4Box 读取的路径以及是否需要调用 cleanup。
// 新式缓存直接返回原路径，实现零额外写入的一步合并。
func prepareM4sInput(src string) (string, bool, error) {
	srcFile, err := os.Open(src)
	if err != nil {
		return "", false, err
	}
	head := make([]byte, len(m4sPadding))
	n, readErr := io.ReadAtLeast(srcFile, head, len(m4sPadding))
	_ = srcFile.Close()
	if readErr != nil || string(head[:n]) != m4sPadding {
		return src, false, nil
	}

	tmpFile, err := os.CreateTemp("", "m4s-*.m4s")
	if err != nil {
		return "", false, err
	}
	tmpPath := tmpFile.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	srcFile, err = os.Open(src)
	if err != nil {
		cleanup()
		return "", false, err
	}
	defer srcFile.Close()
	if _, err = srcFile.Seek(int64(len(m4sPadding)), io.SeekStart); err != nil {
		_ = tmpFile.Close()
		cleanup()
		return "", false, err
	}
	writer := bufio.NewWriter(tmpFile)
	if _, err = io.Copy(writer, srcFile); err != nil {
		_ = tmpFile.Close()
		cleanup()
		return "", false, err
	}
	if err = writer.Flush(); err != nil {
		_ = tmpFile.Close()
		cleanup()
		return "", false, err
	}
	if err = tmpFile.Close(); err != nil {
		cleanup()
		return "", false, err
	}
	logrus.Info("旧式 m4s 缓存已在临时目录剥离头部填充: ", filepath.Base(src))
	return tmpPath, true, nil
}

// composeFromM4s 直接从原始 m4s 分片合并到目标文件，不产生中间文件。
func (c *Config) composeFromM4s(video, audio, outputFile string) (err error) {
	videoInput, videoTemp, err := prepareM4sInput(video)
	if err != nil {
		return err
	}
	if videoTemp {
		defer func() { _ = os.Remove(videoInput) }()
	}
	audioInput, audioTemp, err := prepareM4sInput(audio)
	if err != nil {
		return err
	}
	if audioTemp {
		defer func() { _ = os.Remove(audioInput) }()
	}
	return c.Composition(videoInput, audioInput, outputFile)
}

// GetCachePath 获取用户视频缓存路径
func (c *Config) GetCachePath() {
	if c.findM4sFiles() != nil {
		MessageBox("BiliBili缓存路径 " + c.CachePath + " 未找到缓存文件, \n请重新选择 BiliBili 缓存文件路径！")
		c.SelectDirectory()
		return
	}
	logrus.Info("选择的 BiliBili 缓存目录为: ", c.CachePath)
	return
}

func Size(path string) int64 {
	if utils.IsExist(path) {
		fileInfo, err := os.Stat(path)
		if err != nil {
			return 0
		}
		return fileInfo.Size()
	}
	return 0
}

// Filter 过滤文件名
func Filter(name string, err error) string {
	if err != nil || name == "" {
		return ""
	}
	name = strings.ReplaceAll(name, "（", "(")
	name = strings.ReplaceAll(name, "）", ")")
	name = strings.ReplaceAll(name, "<", "《")
	name = strings.ReplaceAll(name, ">", "》")
	name = strings.ReplaceAll(name, `\`, "#")
	name = strings.ReplaceAll(name, `"`, `'`)
	name = strings.ReplaceAll(name, "/", "#")
	name = strings.ReplaceAll(name, "|", "_")
	name = strings.ReplaceAll(name, "?", "？")
	name = strings.ReplaceAll(name, "*", "-")
	name = strings.ReplaceAll(name, "【", "[")
	name = strings.ReplaceAll(name, "】", "]")
	name = strings.ReplaceAll(name, ":", "：")
	name = strings.ReplaceAll(name, " ", "_")

	return strings.TrimSpace(name)
}

func (c *Config) PanicHandler() {
	if e := recover(); e != nil {
		fmt.Print("按回车键退出...")
		_, _ = fmt.Scanln()
	}
}

// getMp4Metadata 从MP4文件中读取元数据信息
func (c *Config) getMp4Metadata(filePath string) (map[string]string, error) {
	// 构建MP4Box命令行参数
	args := []string{"-info", filePath}
	cmd := execCommandContext(c.context(), c.GPACPath, args...)
	internal.HideChildConsole(cmd)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout

	// 执行命令
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	return parseMP4Metadata(stdout.String()), nil
}

func parseMP4Metadata(output string) map[string]string {
	metadata := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		for _, key := range []string{"title", "artist", "album", "comment"} {
			prefix := key + ":"
			if strings.HasPrefix(lower, prefix) {
				metadata[key] = strings.TrimSpace(line[len(prefix):])
			}
		}
	}
	return metadata
}

// resolveOutputFile 为当前分P分配输出文件名。
// 同一个输出文件夹下多个分P标题相同时，依次使用 part.mp4、part_1.mp4、part_2.mp4…
// 分配前会认领磁盘上已经属于本分P的文件，保证断点续转不会重复生成新序号。
func (c *Config) resolveOutputFile(dir, part string) string {
	if c.assignedOutputs == nil {
		c.assignedOutputs = make(map[string]string)
	}
	for i := 0; ; i++ {
		name := part
		if i > 0 {
			name = fmt.Sprintf("%s_%d", part, i)
		}
		filePath := filepath.Join(dir, name+conver.Mp4Suffix)
		if owner, taken := c.assignedOutputs[filePath]; taken && owner != c.PartId {
			continue // 本次运行中已分配给其他分P
		}
		if !c.isOutputForCurrentPart(filePath) {
			continue // 磁盘上的文件属于其他分P
		}
		c.assignedOutputs[filePath] = c.PartId
		return filePath
	}
}

// isOutputForCurrentPart 判断候选输出文件是否可以被当前分P认领。
// 依据合成时写入 MP4 的元数据（title/artist/album/comment）识别分P身份，
// 不使用任何旁文件。文件不存在时直接可用；元数据缺失的旧文件按候选名认领。
func (c *Config) isOutputForCurrentPart(filePath string) bool {
	if !utils.IsExist(filePath) {
		return true
	}
	if c.skipMetadataCheck {
		return true // 预览模式：存在即认领，避免为每个文件启动 MP4Box。
	}

	metadata, err := c.getMp4Metadata(filePath)
	if err != nil {
		return true // 元数据读不出来时按候选文件名认领，避免重复转换。
	}
	if metadata["title"] != c.GroupId || metadata["artist"] != c.Uid || metadata["album"] != c.ItemId {
		return false
	}
	if comment := metadata["comment"]; comment != "" {
		return comment == c.PartId
	}
	return true // 无分P标记的旧版文件：按候选文件名认领。
}

// isIdenticalFileExists 只在同一个分P内检测重复，避免跨分P误判。
// 分P身份来自合成时写入 MP4 的元数据，不使用任何旁文件。
func (c *Config) isIdenticalFileExists(dirPath string, videoPath string, audioPath string, part string) (bool, string) {
	files, err := os.ReadDir(dirPath)
	if err != nil {
		logrus.Errorf("读取目录失败: %v", err)
		return false, ""
	}

	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(strings.ToLower(file.Name()), conver.Mp4Suffix) {
			continue
		}
		filePath := filepath.Join(dirPath, file.Name())
		metadata, metadataErr := c.getMp4Metadata(filePath)
		if metadataErr != nil || metadata["title"] != c.GroupId || metadata["artist"] != c.Uid || metadata["album"] != c.ItemId {
			continue
		}
		if metadata["comment"] != "" {
			if metadata["comment"] == c.PartId {
				logrus.Info("发现同一分P的已合并文件: ", filePath)
				return true, filePath
			}
			continue
		}
		// 旧MP4没有分P元数据，只允许精确同名匹配，不能使用Contains。
		if file.Name() == part+conver.Mp4Suffix {
			logrus.Info("发现同名的旧版已合并文件: ", filePath)
			return true, filePath
		}
	}
	return false, ""
}

func MessageBox(text string) {
	_ = zenity.Warning(text, zenity.Title("提示"), zenity.Width(400))
}

// findM4sFiles 检查目录及其子目录下是否存在m4s文件
func (c *Config) findM4sFiles() error {
	var m4sFiles []string
	err := filepath.Walk(c.CachePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			logrus.Warnf("查找bilibili缓存目录异常: %s", path)
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == conver.M4sSuffix {
			m4sFiles = append(m4sFiles, path)
			return nil
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(m4sFiles) == 0 {
		return fmt.Errorf("缓存目录找不到m4s文件: %s", c.CachePath)
	}
	return nil
}

// SelectDirectory 选择 BiliBili 缓存目录
func (c *Config) SelectDirectory() {
	for {
		var err error
		c.CachePath, err = zenity.SelectFile(zenity.Title("请选择 BiliBili 缓存目录"), zenity.Directory())
		if c.CachePath == "" || err != nil {
			logrus.Warn("关闭对话框后自动退出程序")
			os.Exit(1)
		}

		if c.findM4sFiles() == nil {
			logrus.Info("选择的 BiliBili 缓存目录为:", c.CachePath)
			return
		}
		MessageBox("选择的 BiliBili 缓存目录内找不到m4s文件，请重新选择！")
	}
}

// SelectGPACPath 选择 GPACPath文件
func (c *Config) SelectGPACPath() {
	for {
		var err error
		c.GPACPath, err = zenity.SelectFile(zenity.Title("请选择 GPAC 的 mp4box 文件"))
		if c.GPACPath == "" || err != nil {
			logrus.Warn("关闭对话框后自动退出程序")
			os.Exit(1)
		}

		if utils.IsExist(c.GPACPath) {
			logrus.Info("选择 GPAC 的 mp4box 文件为:", c.GPACPath)
			return
		}
		MessageBox("选择 GPAC 的 mp4box 文件不存在，请重新选择！")
	}
}

// 如果是目录，尝试下载并转换xml弹幕为ass格式
func (c *Config) downloadXml() {
	dirPath := filepath.Dir(c.video)
	dirName := filepath.Base(dirPath)

	if len(dirName) < 6 { // Android嵌套目录，音视频目录为80
		danmakuXml := filepath.Join(filepath.Dir(dirPath), conver.DanmakuXml)
		if Size(danmakuXml) != 0 {
			c.AssPath = conver.Xml2Ass(danmakuXml) // 转换xml弹幕文件为ass格式
		}
		return
	}
	xmlPath := filepath.Join(dirPath, dirName+conver.XmlSuffix)
	if Size(xmlPath) != 0 {
		c.AssPath = conver.Xml2Ass(xmlPath) // 转换xml弹幕文件为ass格式
		return
	}
	if e := downloadFile(joinUrl(dirName), xmlPath); e != nil {
		if downloadFile(joinXmlUrl(dirName), xmlPath) != nil {
			logrus.Warn("弹幕文件下载失败:", joinUrl(dirName))
			return
		}
	}
	c.AssPath = conver.Xml2Ass(xmlPath) // 转换xml弹幕文件为ass格式
}

// GetVAId 返回.playurl文件中视频文件或音频文件件数组
func GetVAId(patch string) (videoID string, audioID string) {
	pu := filepath.Join(filepath.Dir(patch), conver.PlayUrlSuffix)
	puByte, e := os.ReadFile(pu)
	if e == nil {
		/*
			视频：
			data.dash.video[0].id
			data.dash.audio[0].id
			番剧：
			result.dash.video[0].id  80  需要加上30000，实际30080.m4s
			result.dash.audio[0].id  30280
		*/
		var p gjson.Result
		if p = gjson.GetBytes(puByte, "data"); !p.Exists() {
			p = gjson.GetBytes(puByte, "result")
		}
		if p.Exists() {
			return p.Get("dash.video|@reverse|0.id").String(), p.Get("dash.audio|@reverse|0.id").String()
		}
		return "", ""
	}
	if filepath.Base(filepath.Dir(patch)) != "80" {
		logrus.Warnln("找不到.playurl文件,切换到Android模式解析entry.json文件")
	}
	androidPEJ := filepath.Join(filepath.Dir(filepath.Dir(patch)), conver.PlayEntryJson)
	puDate, e := os.ReadFile(androidPEJ)
	if e != nil {
		logrus.Error("找不到entry.json文件!")
		return
	}
	status := gjson.GetBytes(puDate, "page_data.download_title").String()
	if status != "completed" && status != "视频已缓存完成" && status != "" {
		logrus.Error("跳过未缓存完成的视频", status)
		return
	}
	return "video.m4s", "audio.m4s"
}

func OpenFolder(outputDir string) {
	switch runtime.GOOS {
	case "windows":
		_ = exec.Command("explorer", outputDir).Start()
	case "darwin": // macOS
		_ = exec.Command("open", outputDir).Start()
	default: // Linux and other Unix-like systems
		_ = exec.Command("xdg-open", outputDir).Start()
	}
}
