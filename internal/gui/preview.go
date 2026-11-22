package gui

import (
	"fmt"
	"m4s-converter/common"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lxn/walk"
)

// previewItem 是输出预览树中的节点，同时充当文件夹和文件两种角色。
type previewItem struct {
	text     string
	parent   walk.TreeItem
	children []walk.TreeItem
	folder   bool
}

func (i *previewItem) Text() string                    { return i.text }
func (i *previewItem) Parent() walk.TreeItem           { return i.parent }
func (i *previewItem) ChildCount() int                 { return len(i.children) }
func (i *previewItem) ChildAt(index int) walk.TreeItem { return i.children[index] }
func (i *previewItem) HasChild() bool                  { return len(i.children) > 0 }

// previewModel 把 OutputPlan 转换成可显示的文件夹树。
type previewModel struct {
	walk.TreeModelBase
	roots []walk.TreeItem
}

func (m *previewModel) RootCount() int                 { return len(m.roots) }
func (m *previewModel) RootAt(index int) walk.TreeItem { return m.roots[index] }

func newPreviewModel() *previewModel {
	return &previewModel{}
}

// Clear 清空树，用于未选择目录或目录无效的场景。
func (m *previewModel) Clear() {
	m.roots = nil
	m.PublishItemsReset(nil)
}

// SetPlan 用规划结果重建整棵树。
func (m *previewModel) SetPlan(plan common.OutputPlan) {
	rootName := filepath.Base(strings.TrimRight(plan.OutputDir, `\/`))
	if rootName == "" || rootName == "." || rootName == string(filepath.Separator) {
		rootName = "output"
	}
	root := &previewItem{text: rootName, folder: true}

	files := append([]common.PlannedFile(nil), plan.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Relative < files[j].Relative })

	folders := map[string]*previewItem{"": root}
	for _, file := range files {
		current := root
		key := ""
		for _, segment := range strings.Split(filepath.ToSlash(file.Folder), "/") {
			if segment == "" || segment == "." {
				continue
			}
			key += "/" + segment
			node, ok := folders[key]
			if !ok {
				node = &previewItem{text: segment, parent: current, folder: true}
				current.children = append(current.children, node)
				folders[key] = node
			}
			current = node
		}

		text := file.Name
		if file.Status != "待转换" {
			text += "（" + file.Status + "）"
		}
		current.children = append(current.children, &previewItem{text: text, parent: current})
	}

	m.roots = []walk.TreeItem{root}
	m.PublishItemsReset(nil)
}

// expandFolders 默认展开所有文件夹节点，让预览一眼可见。
func (m *previewModel) expandFolders(tv *walk.TreeView) {
	var visit func(walk.TreeItem)
	visit = func(item walk.TreeItem) {
		for i := 0; i < item.ChildCount(); i++ {
			child := item.ChildAt(i)
			if node, ok := child.(*previewItem); ok && node.folder {
				_ = tv.SetExpanded(child, true)
				visit(child)
			}
		}
	}
	for _, root := range m.roots {
		_ = tv.SetExpanded(root, true)
		visit(root)
	}
}

// previewSummary 生成预览区上方的简短统计文本。
func previewSummary(plan common.OutputPlan) string {
	if len(plan.Files) == 0 {
		if len(plan.Notes) > 0 {
			return "未发现可转换的视频，请检查缓存目录"
		}
		return "这个目录下没有待预览的视频"
	}
	pending, existing, incomplete := 0, 0, 0
	for _, file := range plan.Files {
		switch file.Status {
		case "已存在":
			existing++
		case "缓存未完成":
			incomplete++
		default:
			pending++
		}
	}
	return fmt.Sprintf("共 %d 个文件 · 待转换 %d · 已存在 %d · 缓存未完成 %d",
		len(plan.Files), pending, existing, incomplete)
}
