package common

import (
	"fmt"
	"m4s-converter/conver"
	"m4s-converter/internal"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bitly/go-simplejson"
	"github.com/fatih/color"
	utils "github.com/mzky/utils/common"
	"github.com/sirupsen/logrus"
)

func (c *Config) Synthesis() {
	begin := time.Now().Unix()
	c.assignedOutputs = make(map[string]string)
	logrus.Println("查找缓存目录下可转换的文件...")
	// 直接从原始 m4s 分片合并到输出目录，不在缓存目录生成任何中间文件。

	dirs, err := GetCacheDir(c.CachePath) // 缓存根目录模式
	if err != nil {
		MessageBox(fmt.Sprintf("找不到 BiliBili 的缓存目录：%v", err))
		c.wait()
		if c.NoWait {
			return
		}
	}

	if dirs == nil {
		// 判断非缓存根目录时，验证是否为子目录
		if utils.IsExist(filepath.Join(c.CachePath, conver.VideoInfoSuffix)) ||
			utils.IsExist(filepath.Join(c.CachePath, conver.VideoInfoJson)) {
			dirs = append(dirs, c.CachePath)
		}
	}

	// 合成音视频文件；GUI可指定输出目录，CLI保持原默认行为。
	if c.OutputDir == "" {
		c.OutputDir = filepath.Join(c.CachePath, "output")
	}
	var outputFiles []string
	var skipFilePaths []string
	for _, v := range dirs {
		// 检查是否应该退出
		if c.ShouldExit() {
			logrus.Info("正在退出程序...")
			break
		}
		video, audio, e := c.GetAudioAndVideo(v)
		if e != nil {
			logrus.Error("找不到已修复的音频和视频文件:", e)
			continue
		}
		info := findVideoInfoFile(v)
		if info == "" {
			continue
		}
		infoStr, e := os.ReadFile(info)
		if e != nil {
			logrus.Error("找不到包含视频信息的info相关文件: ", info)
			continue
		}
		js, e := simplejson.NewJson(infoStr)
		if e != nil {
			logrus.Error("videoInfo相关文件解析失败: ", info)
			continue
		}

		vi := parseVideoInfo(js, v)
		c.ItemId, c.GroupId, c.Uid, c.PartId = vi.ItemId, vi.GroupId, vi.Uid, vi.PartId
		groupTitle, title, part, uname, status := vi.GroupTitle, vi.Title, vi.Part, vi.UName, vi.Status

		if cacheIncomplete(status) {
			displayPart := null2Str(part, title)
			c.emit(ConversionEvent{CacheDir: v, Group: groupTitle, Part: displayPart, Status: StatusSkipped, Progress: 100, Err: "缓存未完成"})
			skipFilePaths = append(skipFilePaths, v)
			logrus.Warn("未缓存完成,跳过合成", v, title+"-"+uname)
			continue
		}
		if !utils.IsExist(c.OutputDir) {
			_ = os.MkdirAll(c.OutputDir, os.ModePerm)
		}
		relGroupPath := outputGroupPath(vi, c.GroupByUP)
		groupDir := filepath.Join(c.OutputDir, relGroupPath)
		if !utils.IsExist(groupDir) {
			if err = os.MkdirAll(groupDir, os.ModePerm); err != nil {
				MessageBox("无法创建目录：" + groupDir)
				c.wait()
				if c.NoWait {
					return
				}
			}
		}
		// 生成输出文件名；同一文件夹下同名分P自动追加 _1、_2、_3… 后缀。
		if part == "" {
			part = title
		}
		outputFile := c.resolveOutputFile(groupDir, part)
		mp4Name := filepath.Base(outputFile)

		// 设置元数据信息
		c.Title = title
		c.Uname = uname
		c.GroupTitle = groupTitle
		event := ConversionEvent{CacheDir: v, Group: groupTitle, Part: part, OutputFile: outputFile, Status: StatusPreparing, Progress: 10}
		c.emit(event)

		// 检查是否已经存在已合并文件
		if utils.IsExist(outputFile) {
			event.Status = StatusSkipped
			event.Progress = 100
			event.Err = "输出文件已存在，断点续转时跳过"
			c.emit(event)
			// 提取已合并文件的元数据
			metadata, getErr := c.getMp4Metadata(outputFile)
			if getErr == nil {
				// 验证三个值是否一致
				if metadata["title"] == c.GroupId && metadata["artist"] == c.Uid && metadata["album"] == c.ItemId {
					logrus.Warn("跳过已合并文件: ", outputFile)
					continue
				}
			}
			// 如果元数据提取失败或验证失败，仍然跳过同名文件
			logrus.Warn("跳过已合并的视频: ", outputFile)
			continue
		}

		// 检查目录中是否存在与输入音频和视频文件内容相同的文件
		if exists, existingFile := c.isIdenticalFileExists(groupDir, video, audio, part); exists {
			event.Status = StatusSkipped
			event.Progress = 100
			event.Err = "同一分P存在相同内容: " + existingFile
			c.emit(event)
			logrus.Warn("跳过完全相同的视频: ", existingFile)
			continue
		}

		// 执行合成
		event.Status = StatusRunning
		event.Progress = 50
		c.emit(event)
		if er := c.composeFromM4s(video, audio, outputFile); er != nil {
			event.Progress = 100
			event.Err = er.Error()
			if c.ShouldExit() {
				event.Status = StatusCancelled
			} else {
				event.Status = StatusFailed
			}
			c.emit(event)
			logrus.Errorf("%s 合成失败", filepath.Base(outputFile))
			continue
		}

		event.Status = StatusCompleted
		event.Progress = 100
		c.emit(event)
		outputFiles = append(outputFiles, filepath.Join(relGroupPath, mp4Name))
	}

	// 处理未合并的MP3和视频文件
	if c.Summarize && !c.ShouldExit() {
		// 查找未合并的MP3和视频文件
		for _, v := range dirs {
			video, audio, e := c.GetAudioAndVideo(v)
			if e != nil {
				continue
			}

			// 检查是否已经合成
			info := findVideoInfoFile(v)
			if info == "" {
				continue
			}
			infoStr, e := os.ReadFile(info)
			if e != nil {
				continue
			}
			js, e := simplejson.NewJson(infoStr)
			if e != nil {
				continue
			}

			vi := parseVideoInfo(js, v)
			title := vi.Title

			// 添加空值检查，避免创建空目录名或尝试复制空文件路径
			if vi.OwnerName == "" && vi.GroupTitle == "" && vi.RawGroup == "" {
				logrus.Warn("项目信息为空，跳过处理未合并文件: ", v)
				continue
			}

			// 创建项目特定的未合并文件夹
			groupDir := filepath.Join(c.OutputDir, outputGroupPath(vi, c.GroupByUP))
			if !utils.IsExist(groupDir) {
				if err = os.MkdirAll(groupDir, os.ModePerm); err != nil {
					logrus.Error("创建项目目录失败: ", err)
					continue
				}
			}
			summaryDir := filepath.Join(groupDir, "未合并文件")
			if !utils.IsExist(summaryDir) {
				if err = os.MkdirAll(summaryDir, os.ModePerm); err != nil {
					logrus.Error("创建未合并文件目录失败: ", err)
					continue
				}
			}

			// 复制未合并的视频文件
			if utils.IsExist(video) && title != "" {
				videoDest := filepath.Join(summaryDir, title+"_video"+filepath.Ext(video))
				if !utils.IsExist(videoDest) {
					if err := c.copyFile(video, videoDest); err == nil {
						logrus.Info("已将未合并的视频文件放入汇总目录: ", videoDest)
					}
				} else {
					logrus.Warn("未合并的视频文件已存在，跳过复制: ", videoDest)
				}
			}

			// 复制未合并的音频文件
			if utils.IsExist(audio) && title != "" {
				audioDest := filepath.Join(summaryDir, title+"_audio"+filepath.Ext(audio))
				if !utils.IsExist(audioDest) {
					if err := c.copyFile(audio, audioDest); err == nil {
						logrus.Info("已将未合并的音频文件放入汇总目录: ", audioDest)
					}
				} else {
					logrus.Warn("未合并的音频文件已存在，跳过复制: ", audioDest)
				}
			}
		}
	}

	end := time.Now().Unix()
	logrus.Print("===========================================")
	if skipFilePaths != nil {
		logrus.Print("跳过的目录:\n" + strings.Join(skipFilePaths, "\n"))
	}
	if outputFiles != nil {
		logrus.Printf("# 输出目录:\n%s", color.CyanString(c.OutputDir))
		logrus.Printf("# 合成的文件:\n%s", color.CyanString(strings.Join(outputFiles, "\n")))
		// 保持CLI原行为；GUI由用户主动点击“打开输出目录”。
		if !c.NoWait && !c.ShouldExit() {
			go OpenFolder(c.OutputDir)
		}
	} else {
		logrus.Warn("未合成任何文件！")
	}
	logrus.Print("===========================================")
	logrus.Print("已完成合成任务，耗时: ", end-begin, "秒")
	c.wait()
}

func (c *Config) findMp4Info(fp, sub string) bool {
	if !utils.IsExist(c.GPACPath) {
		return false
	}
	cmd := exec.Command(c.GPACPath, "-info", fp)
	internal.HideChildConsole(cmd)
	ret, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(ret), sub)
}

func null2Str(s string, value string) string {
	if s != "" {
		return s
	}
	return value
}

func jsonIdentifier(js *simplejson.Json, path ...string) string {
	value := js.GetPath(path...)
	if text, err := value.String(); err == nil && text != "" {
		return text
	}
	if number, err := value.Int(); err == nil {
		return strconv.Itoa(number)
	}
	return ""
}

// partIdentity 返回稳定且不含MP4Box标签分隔符的分P身份。
// 优先使用B站cid，其次使用页码；老缓存缺少两者时才退回目录名。
func partIdentity(js *simplejson.Json, cacheDir string) string {
	if cid := null2Str(jsonIdentifier(js, "page_data", "cid"), jsonIdentifier(js, "cid")); cid != "" {
		return "cid-" + cid
	}
	if page := null2Str(jsonIdentifier(js, "page_data", "page"), jsonIdentifier(js, "page")); page != "" {
		return "page-" + page
	}
	return "dir-" + filepath.Base(cacheDir)
}

func (c *Config) wait() {
	if c.NoWait {
		return
	}
	fmt.Println("按任意键退出程序")
	_, _ = fmt.Scanln()
	os.Exit(0)
}
