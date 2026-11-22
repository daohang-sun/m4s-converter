package common

// ConversionStatus 表示单个分P的转换状态。
type ConversionStatus string

const (
	StatusPreparing ConversionStatus = "准备中"
	StatusRunning   ConversionStatus = "转换中"
	StatusCompleted ConversionStatus = "已完成"
	StatusSkipped   ConversionStatus = "已跳过"
	StatusFailed    ConversionStatus = "失败"
	StatusCancelled ConversionStatus = "已取消"
)

// ConversionEvent 是核心转换流程向CLI/GUI发布的轻量状态通知。
// CacheDir 是分P缓存目录，也是同一批次内的稳定任务标识。
type ConversionEvent struct {
	CacheDir   string
	Group      string
	Part       string
	OutputFile string
	Status     ConversionStatus
	Progress   int
	Err        string
}

func (c *Config) emit(event ConversionEvent) {
	if c.Observer != nil {
		c.Observer(event)
	}
}
