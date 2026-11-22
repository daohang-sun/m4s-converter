package common

import (
	"fmt"
	"m4s-converter/conver"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bitly/go-simplejson"
	utils "github.com/mzky/utils/common"
)

// videoInfo 汇总转换流程与输出预览共用的视频元信息。
type videoInfo struct {
	GroupTitle string // 默认文件夹名第一段（空时回退 owner_name）
	Title      string // 视频标题
	Part       string
	UName      string // 默认文件夹名第二段（空时回退 title）
	OwnerName  string // 真正的 UP 主名，来自 owner_name 字段
	RawGroup   string // groupTitle 字段原始值，作为合集名
	Status     string
	ItemId     string
	GroupId    string
	Uid        string
	PartId     string
}

// parseVideoInfo 解析 B 站缓存的 info 文件。Synthesis 与 BuildOutputPlan 共用，
// 保证预览中看到的文件名与实际转换生成的文件名完全一致。
func parseVideoInfo(js *simplejson.Json, cacheDir string) videoInfo {
	rawGroup := Filter(js.Get("groupTitle").String())
	groupTitle := null2Str(rawGroup, Filter(js.Get("owner_name").String()))
	title := null2Str(Filter(js.Get("page_data").Get("download_subtitle").String()), Filter(js.Get("title").String()))
	part := Filter(js.Get("page_data").Get("part").String())
	uname := null2Str(Filter(js.Get("uname").String()), Filter(js.Get("title").String()))
	status := null2Str(Filter(js.Get("status").String()), Filter(js.Get("page_data").Get("download_title").String()))

	// UP 主名优先取 owner_name；旧缓存可能只有 uname，再回退到 groupTitle。
	ownerName := null2Str(Filter(js.Get("owner_name").String()), Filter(js.Get("uname").String()))
	ownerName = null2Str(ownerName, groupTitle)

	itemId, e := js.Get("itemId").Int()
	if itemId == 0 || e != nil {
		itemId, _ = js.Get("owner_id").Int()
	}

	return videoInfo{
		GroupTitle: groupTitle,
		Title:      title,
		Part:       part,
		UName:      uname,
		OwnerName:  ownerName,
		RawGroup:   rawGroup,
		Status:     status,
		ItemId:     strconv.Itoa(itemId),
		GroupId:    Filter(js.Get("groupId").String()),
		Uid:        Filter(js.Get("uid").String()),
		PartId:     partIdentity(js, cacheDir),
	}
}

// findVideoInfoFile 在分 P 缓存目录中查找信息文件。
func findVideoInfoFile(dir string) string {
	for _, name := range []string{conver.VideoInfoJson, conver.VideoInfoSuffix, conver.PlayEntryJson} {
		path := filepath.Join(dir, name)
		if utils.IsExist(path) {
			return path
		}
	}
	return ""
}

// cacheIncomplete 判断缓存是否未完成（未完成的份额不参与转换）。
func cacheIncomplete(status string) bool {
	return status != "completed" && status != "视频已缓存完成" && status != ""
}

// outputGroupPath 返回该视频在输出目录下的相对文件夹路径。
// 默认：合集-UP主（保持原行为）；勾选归类后：UP主(owner_name)/合集(groupTitle)/分P。
func outputGroupPath(info videoInfo, byUP bool) string {
	if !byUP {
		return info.GroupTitle + "-" + info.UName
	}

	// UP 主名取 owner_name，旧缓存回退到 uname/groupTitle。
	up := info.OwnerName
	// 合集名取 groupTitle 字段原始值；没有合集时用视频标题，避免空目录。
	collection := info.RawGroup
	if collection == "" {
		collection = info.Title
	}
	if up == "" {
		up = "未分组"
	}
	if collection == "" {
		collection = "未命名合集"
	}
	if up == collection {
		return up
	}
	return filepath.Join(up, collection)
}

// PlannedFile 描述一个预计生成的输出文件。
type PlannedFile struct {
	Group    string `json:"group"`    // 合集名
	UName    string `json:"uName"`    // UP 主名
	Part     string `json:"part"`     // 分 P 显示名
	Folder   string `json:"folder"`   // 相对输出目录的文件夹路径
	Name     string `json:"name"`     // 文件名
	Relative string `json:"relative"` // 相对输出目录的完整路径
	Status   string `json:"status"`   // 待转换 / 已存在 / 缓存未完成
	CacheDir string `json:"cacheDir"` // 对应的缓存分 P 目录
}

// OutputPlan 是选择缓存目录后计算出的输出文件夹树。
type OutputPlan struct {
	OutputDir string        `json:"outputDir"`
	Files     []PlannedFile `json:"files"`
	Notes     []string      `json:"notes"`
}

// BuildOutputPlan 只读取缓存目录，计算将会生成的输出文件树，不创建或修改任何文件。
// gpacPath 留空即可：预览阶段不需要读取已有 MP4 的元数据，按候选文件名认领。
func BuildOutputPlan(cachePath, outputDir string, groupByUP bool) OutputPlan {
	plan := OutputPlan{OutputDir: outputDir}
	cachePath = filepath.Clean(strings.TrimSpace(cachePath))
	if cachePath == "." || cachePath == "" {
		return plan
	}

	c := &Config{
		CachePath:         cachePath,
		OutputDir:         outputDir,
		AssOFF:            true, // 预览时不要触发弹幕下载
		NoWait:            true,
		GroupByUP:         groupByUP,
		skipMetadataCheck: true,
	}
	c.assignedOutputs = make(map[string]string)

	dirs, err := GetCacheDir(cachePath)
	if err != nil {
		dirs = nil
	}
	if len(dirs) == 0 && (utils.IsExist(filepath.Join(cachePath, conver.VideoInfoSuffix)) ||
		utils.IsExist(filepath.Join(cachePath, conver.VideoInfoJson))) {
		dirs = []string{cachePath}
	}
	if len(dirs) == 0 {
		plan.Notes = append(plan.Notes, "未找到可转换的分 P 目录")
		return plan
	}

	for _, dir := range dirs {
		if _, _, e := c.GetAudioAndVideo(dir); e != nil {
			plan.Notes = append(plan.Notes, fmt.Sprintf("跳过（缺少音视频分片）: %s", filepath.Base(dir)))
			continue
		}
		infoFile := findVideoInfoFile(dir)
		if infoFile == "" {
			continue
		}
		data, e := os.ReadFile(infoFile)
		if e != nil {
			continue
		}
		js, e := simplejson.NewJson(data)
		if e != nil {
			continue
		}

		vi := parseVideoInfo(js, dir)
		c.ItemId, c.GroupId, c.Uid, c.PartId = vi.ItemId, vi.GroupId, vi.Uid, vi.PartId

		displayPart := null2Str(vi.Part, vi.Title)
		if displayPart == "" {
			displayPart = "未命名分P"
		}
		part := vi.Part
		if part == "" {
			part = vi.Title
		}
		if part == "" {
			part = "未命名分P"
		}

		folder := outputGroupPath(vi, groupByUP)
		absDir := filepath.Join(outputDir, folder)
		var output string
		if cacheIncomplete(vi.Status) {
			// 与转换流程一致：未完成的分 P 不占用文件名。
			output = peekOutputFile(c, absDir, part)
		} else {
			output = c.resolveOutputFile(absDir, part)
		}

		status := "待转换"
		if cacheIncomplete(vi.Status) {
			status = "缓存未完成"
		} else if utils.IsExist(output) {
			status = "已存在"
		}

		name := filepath.Base(output)
		plan.Files = append(plan.Files, PlannedFile{
			Group:    vi.GroupTitle,
			UName:    vi.UName,
			Part:     displayPart,
			Folder:   folder,
			Name:     name,
			Relative: filepath.Join(folder, name),
			Status:   status,
			CacheDir: dir,
		})
	}
	return plan
}

// peekOutputFile 计算候选输出文件名但不占用它，供未完成的分 P 预览使用。
func peekOutputFile(c *Config, dir, part string) string {
	for i := 0; ; i++ {
		name := part
		if i > 0 {
			name = fmt.Sprintf("%s_%d", part, i)
		}
		path := filepath.Join(dir, name+conver.Mp4Suffix)
		if owner, taken := c.assignedOutputs[path]; taken && owner != c.PartId {
			continue
		}
		if !c.isOutputForCurrentPart(path) {
			continue
		}
		return path
	}
}
