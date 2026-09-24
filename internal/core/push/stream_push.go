package push

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gowvp/owl/internal/core/bz"
	"github.com/ixugo/goddd/pkg/hook"
	"github.com/ixugo/goddd/pkg/orm"
	"github.com/ixugo/goddd/pkg/reason"
	"github.com/jinzhu/copier"
)

// StreamPushStorer 推流持久化接口
type StreamPushStorer interface {
	WithTx(orm.Tx) (StreamPushStorer, error)
	Create(context.Context, *StreamPush) error
	Update(context.Context, *StreamPush, func(*StreamPush) error) error
	Delete(context.Context, *StreamPush) error
	List(context.Context, *ListStreamPushInput) ([]*StreamPush, int64, error)
	GetByID(context.Context, string) (*StreamPush, error)
	GetByAppStream(ctx context.Context, app, stream string) (*StreamPush, error)
}

// ListStreamPushs 分页查询推流列表
func (c Core) ListStreamPushs(ctx context.Context, in *ListStreamPushInput) ([]*StreamPush, int64, error) {
	items, total, err := c.store.StreamPush().List(ctx, in)
	if err != nil {
		return nil, 0, reason.ErrDB.Withf("List err[%s]", err.Error())
	}
	if items == nil {
		items = []*StreamPush{}
	}
	return items, total, nil
}

// GetStreamPush 按 ID 查询单条
func (c Core) GetStreamPush(ctx context.Context, id string) (*StreamPush, error) {
	out, err := c.store.StreamPush().GetByID(ctx, id)
	if err != nil {
		if orm.IsErrRecordNotFound(err) {
			return nil, reason.ErrNotFound.Withf("Get id[%v] err[%s]", id, err.Error())
		}
		return nil, reason.ErrDB.Withf("Get id[%v] err[%s]", id, err.Error())
	}
	return out, nil
}

// GetStreamPushByAppStream 按 app + stream 查询单条
func (c Core) GetStreamPushByAppStream(ctx context.Context, app, stream string) (*StreamPush, error) {
	out, err := c.store.StreamPush().GetByAppStream(ctx, app, stream)
	if err != nil {
		if orm.IsErrRecordNotFound(err) {
			return nil, reason.ErrNotFound.Withf("Get app[%s] stream[%s] err[%s]", app, stream, err.Error())
		}
		return nil, reason.ErrDB.Withf("Get app[%s] stream[%s] err[%s]", app, stream, err.Error())
	}
	return out, nil
}

// CreateStreamPush 创建推流
func (c Core) CreateStreamPush(ctx context.Context, in *CreateStreamPushInput) (*StreamPush, error) {
	var out StreamPush
	if err := copier.Copy(&out, in); err != nil {
		slog.ErrorContext(ctx, "Copy", "err", err)
	}
	out.ID = c.uniqueID.UniqueID(bz.IDPrefixRTMP)
	if err := c.store.StreamPush().Create(ctx, &out); err != nil {
		if orm.IsDuplicatedKey(err) {
			return nil, reason.ErrDB.WithMsg("stream 重复，请勿重复添加")
		}
		return nil, reason.ErrDB.Withf("Create err[%s]", err.Error())
	}
	return &out, nil
}

// UpdateStreamPush 更新推流信息
func (c Core) UpdateStreamPush(ctx context.Context, in *UpdateStreamPushInput) (*StreamPush, error) {
	out := StreamPush{ID: in.ID}
	if err := c.store.StreamPush().Update(ctx, &out, func(b *StreamPush) error {
		if err := copier.Copy(b, in); err != nil {
			slog.ErrorContext(ctx, "Copy", "err", err)
		}
		return nil
	}); err != nil {
		return nil, reason.ErrDB.Withf("Update id[%v] err[%s]", in.ID, err.Error())
	}
	return &out, nil
}

// DeleteStreamPush 删除推流
func (c Core) DeleteStreamPush(ctx context.Context, id string) (*StreamPush, error) {
	out := StreamPush{ID: id}
	if err := c.store.StreamPush().Delete(ctx, &out); err != nil {
		return nil, reason.ErrDB.Withf("Delete id[%v] err[%s]", id, err.Error())
	}
	return &out, nil
}

// PublishInput 推流鉴权输入
type PublishInput struct {
	App           string
	Stream        string
	MediaServerID string
	Sign          string
	Secret        string
	Session       string
}

// Publish 推流上线：鉴权 + 更新状态
func (c Core) Publish(ctx context.Context, in PublishInput) error {
	result, err := c.GetStreamPushByAppStream(ctx, in.App, in.Stream)
	if err != nil {
		return err
	}
	if !result.IsAuthDisabled {
		if s := hook.MD5(in.Session + in.Secret); s != in.Sign {
			slog.Info("推流鉴权失败", "got", in.Sign, "expect", s)
			return fmt.Errorf("Unauthorized, rtmp secret error, got[%s]", in.Sign)
		}
	}

	s := StreamPush{ID: result.ID}
	return c.store.StreamPush().Update(ctx, &s, func(b *StreamPush) error {
		b.MediaServerID = in.MediaServerID
		b.Status = StatusPushing
		now := orm.Now()
		b.PushedAt = &now
		b.Session = in.Session
		return nil
	})
}

// UnPublish 推流下线：更新状态
func (c Core) UnPublish(ctx context.Context, app, stream string) error {
	result, err := c.GetStreamPushByAppStream(ctx, app, stream)
	if err != nil {
		return err
	}
	s := StreamPush{ID: result.ID}
	return c.store.StreamPush().Update(ctx, &s, func(b *StreamPush) error {
		b.Status = StatusStopped
		now := orm.Now()
		b.StoppedAt = &now
		b.Session = ""
		return nil
	})
}

// OnPlayInput 拉流鉴权输入
type OnPlayInput struct {
	App     string
	Stream  string
	Session string
}

// OnPlay 拉流鉴权
func (c Core) OnPlay(ctx context.Context, in OnPlayInput) error {
	result, err := c.GetStreamPushByAppStream(ctx, in.App, in.Stream)
	if err != nil {
		return err
	}
	if result.IsAuthDisabled {
		return nil
	}
	if in.Session != result.Session {
		slog.Info("拉流鉴权失败", "got", in.Session, "expect", result.Session)
		return fmt.Errorf("Unauthorized, session error, got[%s]", in.Session)
	}
	return nil
}
