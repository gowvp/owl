package recording

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/ixugo/goddd/pkg/orm"
	"github.com/ixugo/goddd/pkg/reason"
	"github.com/ixugo/goddd/pkg/web"
	"github.com/jinzhu/copier"
	"gorm.io/gorm"
)

// RecordingStorer 录像实体持久化接口
type RecordingStorer interface {
	WithTx(orm.Tx) (RecordingStorer, error)
	Create(context.Context, *Recording) error
	Update(context.Context, *Recording, func(*Recording) error) error
	Delete(context.Context, *Recording) error
	List(context.Context, *FindRecordingInput) ([]*Recording, int64, error)
	Count(context.Context, *FindRecordingInput) (int64, error)
	GetByID(context.Context, int64) (*Recording, error)

	Session(context.Context, ...func(*gorm.DB) error) error
}

// ListRecordings 分页查询录像列表，支持通道ID和时间范围筛选
func (c Core) ListRecordings(ctx context.Context, in *FindRecordingInput) ([]*Recording, int64, error) {
	in.OrderBy = "started_at DESC"

	items, total, err := c.store.Recording().List(ctx, in)
	if err != nil {
		return nil, 0, reason.ErrDB.Withf(`Find in[%+v] err[%s]`, in, err.Error())
	}
	// 兼容旧数据：历史 Path 含 storageDir 前缀（如 configs/recordings/rtp/...），
	// 拼接播放 URL 前须去除，否则与 /static/recordings 前缀重复致 404
	prefix := ""
	if c.conf != nil && c.conf.StorageDir != "" {
		prefix = strings.TrimPrefix(filepath.Clean(c.conf.StorageDir), "./") + "/"
	}
	for _, item := range items {
		if prefix != "" {
			item.Path = strings.TrimPrefix(item.Path, prefix)
		}
		if ctx, ok := ctx.(web.Context); ok {
			item.Path = ctx.BaseURLJoin("/static/recordings", item.Path)
		}
	}
	if items == nil {
		items = []*Recording{}
	}
	return items, total, nil
}

// GetRecording 按 ID 查询
func (c Core) GetRecording(ctx context.Context, id int64) (*Recording, error) {
	out, err := c.store.Recording().GetByID(ctx, id)
	if err != nil {
		if orm.IsErrRecordNotFound(err) {
			return nil, reason.ErrNotFound.Withf(`Get id[%v] err[%s]`, id, err.Error())
		}
		return nil, reason.ErrDB.Withf(`Get id[%v] err[%s]`, id, err.Error())
	}
	return out, nil
}

// CreateRecording 创建录像记录
func (c Core) CreateRecording(ctx context.Context, in *AddRecordingInput) (*Recording, error) {
	var out Recording
	if err := copier.Copy(&out, in); err != nil {
		slog.ErrorContext(ctx, "Copy", "err", err)
	}

	if err := c.store.Recording().Create(ctx, &out); err != nil {
		return nil, reason.ErrDB.Withf(`Create err[%s]`, err.Error())
	}
	return &out, nil
}

// UpdateRecording 更新录像记录
func (c Core) UpdateRecording(ctx context.Context, in *EditRecordingInput, id int64) (*Recording, error) {
	out := Recording{ID: id}
	if err := c.store.Recording().Update(ctx, &out, func(b *Recording) error {
		return copier.Copy(b, in)
	}); err != nil {
		return nil, reason.ErrDB.Withf(`Edit id[%v] err[%s]`, id, err.Error())
	}
	return &out, nil
}

// DeleteRecording 删除录像记录
func (c Core) DeleteRecording(ctx context.Context, id int64) (*Recording, error) {
	out := Recording{ID: id}
	if err := c.store.Recording().Delete(ctx, &out); err != nil {
		return nil, reason.ErrDB.Withf(`Del id[%v] err[%s]`, id, err.Error())
	}
	return &out, nil
}

// GetTimeline 获取时间轴数据，返回指定时间范围内的录像时段列表
func (c Core) GetTimeline(ctx context.Context, in *TimelineInput) ([]TimeRange, error) {
	if in.CID == "" {
		return nil, reason.ErrBadRequest.Withf("cid is required")
	}
	if in.StartMs <= 0 || in.EndMs <= 0 {
		return nil, reason.ErrBadRequest.Withf("start_ms and end_ms are required")
	}

	endAt := in.EndAt()
	startAt := in.StartAt()
	recordings, _, err := c.store.Recording().List(ctx, &FindRecordingInput{
		Page: 1, Size: 1000,
		CID:             in.CID,
		StartedAtBefore: &endAt,
		EndedAtAfter:    &startAt,
		OrderBy:         "started_at ASC",
	})
	if err != nil {
		return nil, reason.ErrDB.Withf(`GetTimeline err[%s]`, err.Error())
	}

	result := make([]TimeRange, 0, len(recordings))
	for _, r := range recordings {
		result = append(result, TimeRange{
			ID:          r.ID,
			StartMs:     r.StartedAt.UnixMilli(),
			EndMs:       r.EndedAt.UnixMilli(),
			Duration:    r.Duration,
			ObjectCount: r.ObjectCount,
			DeleteFlag:  r.DeleteFlag,
		})
	}
	return result, nil
}

// cidCount 用于接收 GROUP BY 查询结果
type cidCount struct {
	CID   string `gorm:"column:cid"`
	Count int64  `gorm:"column:cnt"`
}

// HasRecordings 批量检查通道是否有录像
func (c Core) HasRecordings(ctx context.Context, cids []string) (map[string]bool, error) {
	result := make(map[string]bool, len(cids))
	if len(cids) == 0 {
		return result, nil
	}

	var counts []cidCount
	err := c.store.Recording().Session(ctx, func(db *gorm.DB) error {
		return db.Model(&Recording{}).
			Select("cid, COUNT(*) as cnt").
			Where("cid IN ?", cids).
			Group("cid").
			Find(&counts).Error
	})
	if err != nil {
		return result, err
	}

	for _, c := range counts {
		result[c.CID] = c.Count > 0
	}
	return result, nil
}

// GetMonthlyStats 获取月度录像统计
func (c Core) GetMonthlyStats(ctx context.Context, in *MonthlyStatsInput) (*MonthlyStatsOutput, error) {
	if in.Year <= 0 || in.Month < 1 || in.Month > 12 {
		return nil, reason.ErrBadRequest.Withf("invalid year or month")
	}

	firstDay := time.Date(in.Year, time.Month(in.Month), 1, 0, 0, 0, 0, time.Local)
	lastDay := firstDay.AddDate(0, 1, 0).Add(-time.Nanosecond)
	daysInMonth := lastDay.Day()

	recordings, _, err := c.store.Recording().List(ctx, &FindRecordingInput{
		Page: 1, Size: 10000,
		CID:             in.CID,
		StartedAtAfter:  &firstDay,
		StartedAtBefore: &lastDay,
	})
	if err != nil {
		return nil, reason.ErrDB.Withf(`GetMonthlyStats err[%s]`, err.Error())
	}

	dayHasVideo := make([]bool, daysInMonth)
	for _, r := range recordings {
		day := r.StartedAt.Day()
		if day >= 1 && day <= daysInMonth {
			dayHasVideo[day-1] = true
		}
	}

	bitmap := make([]byte, daysInMonth)
	for i, has := range dayHasVideo {
		if has {
			bitmap[i] = '1'
		} else {
			bitmap[i] = '0'
		}
	}

	return &MonthlyStatsOutput{
		Year:     in.Year,
		Month:    in.Month,
		Days:     daysInMonth,
		HasVideo: string(bitmap),
	}, nil
}
