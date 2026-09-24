package event

import (
	"context"
	"log/slog"

	"github.com/ixugo/goddd/pkg/orm"
	"github.com/ixugo/goddd/pkg/reason"
	"github.com/ixugo/goddd/pkg/web"
	"github.com/jinzhu/copier"
)

// EventStorer 事件持久化接口
type EventStorer interface {
	WithTx(orm.Tx) (EventStorer, error)
	Create(context.Context, *Event) error
	Update(context.Context, *Event, func(*Event) error) error
	Delete(context.Context, *Event) error
	List(context.Context, *ListEventInput) ([]*Event, int64, error)
	Count(context.Context, *ListEventInput) (int64, error)
	GetByID(context.Context, int64) (*Event, error)
	BatchDeleteByIDs(context.Context, []int64) error
}

// ListEvents 分页查询事件列表，支持按 CID 和时间范围筛选
func (c Core) ListEvents(ctx context.Context, in *ListEventInput) ([]*Event, int64, error) {
	items, total, err := c.store.Event().List(ctx, in)
	if err != nil {
		return nil, 0, reason.ErrDB.Withf("Find in[%+v] err[%s]", in, err.Error())
	}
	for _, item := range items {
		if ctx, ok := ctx.(web.Context); ok {
			item.ImagePath = ctx.BaseURLJoin("/events/image/", item.ImagePath)
		}
	}
	if items == nil {
		items = []*Event{}
	}
	return items, total, nil
}

// GetEvent 根据 ID 查询单个事件
func (c Core) GetEvent(ctx context.Context, id int64) (*Event, error) {
	out, err := c.store.Event().GetByID(ctx, id)
	if err != nil {
		if orm.IsErrRecordNotFound(err) {
			return nil, reason.ErrNotFound.Withf("Get id[%v] err[%s]", id, err.Error())
		}
		return nil, reason.ErrDB.Withf("Get id[%v] err[%s]", id, err.Error())
	}
	return out, nil
}

// CreateEvent 新增事件记录
func (c Core) CreateEvent(ctx context.Context, in *CreateEventInput) (*Event, error) {
	var out Event
	if err := copier.Copy(&out, in); err != nil {
		slog.ErrorContext(ctx, "Copy", "err", err)
	}

	if err := c.store.Event().Create(ctx, &out); err != nil {
		return nil, reason.ErrDB.Withf("Create err[%s]", err.Error())
	}
	return &out, nil
}

// UpdateEvent 更新事件信息
func (c Core) UpdateEvent(ctx context.Context, in *UpdateEventInput) (*Event, error) {
	out := Event{ID: in.ID}
	if err := c.store.Event().Update(ctx, &out, func(b *Event) error {
		if !in.EndedAt.IsZero() {
			b.EndedAt = in.EndedAt
		}
		return nil
	}); err != nil {
		return nil, reason.ErrDB.Withf("Edit id[%v] err[%s]", in.ID, err.Error())
	}
	return &out, nil
}

// DeleteEvent 删除事件
func (c Core) DeleteEvent(ctx context.Context, id int64) (*Event, error) {
	out := Event{ID: id}
	if err := c.store.Event().Delete(ctx, &out); err != nil {
		return nil, reason.ErrDB.Withf("Del id[%v] err[%s]", id, err.Error())
	}
	return &out, nil
}
