package proxy

import (
	"context"
	"log/slog"

	"github.com/gowvp/owl/internal/core/bz"
	"github.com/ixugo/goddd/pkg/orm"
	"github.com/ixugo/goddd/pkg/reason"
	"github.com/jinzhu/copier"
)

// StreamProxyStorer 拉流代理持久化接口
type StreamProxyStorer interface {
	WithTx(orm.Tx) (StreamProxyStorer, error)
	Create(context.Context, *StreamProxy) error
	Update(context.Context, *StreamProxy, func(*StreamProxy) error) error
	Delete(context.Context, *StreamProxy) error
	List(context.Context, *ListStreamProxyInput) ([]*StreamProxy, int64, error)
	GetByID(context.Context, string) (*StreamProxy, error)
	GetByAppStream(ctx context.Context, app, stream string) (*StreamProxy, error)
}

// ListStreamProxys 分页查询拉流代理列表
func (c *Core) ListStreamProxys(ctx context.Context, in *ListStreamProxyInput) ([]*StreamProxy, int64, error) {
	items, total, err := c.store.StreamProxy().List(ctx, in)
	if err != nil {
		return nil, 0, reason.ErrDB.Withf("List err[%s]", err.Error())
	}
	if items == nil {
		items = []*StreamProxy{}
	}
	return items, total, nil
}

// GetStreamProxy 按 ID 查询单条
func (c *Core) GetStreamProxy(ctx context.Context, id string) (*StreamProxy, error) {
	out, err := c.store.StreamProxy().GetByID(ctx, id)
	if err != nil {
		if orm.IsErrRecordNotFound(err) {
			return nil, reason.ErrNotFound.Withf("Get id[%v] err[%s]", id, err.Error())
		}
		return nil, reason.ErrDB.Withf("Get id[%v] err[%s]", id, err.Error())
	}
	return out, nil
}

// GetStreamProxyByAppStream 按 app + stream 查询单条
func (c *Core) GetStreamProxyByAppStream(ctx context.Context, app, stream string) (*StreamProxy, error) {
	out, err := c.store.StreamProxy().GetByAppStream(ctx, app, stream)
	if err != nil {
		if orm.IsErrRecordNotFound(err) {
			return nil, reason.ErrNotFound.Withf("Get app[%s] stream[%s] err[%s]", app, stream, err.Error())
		}
		return nil, reason.ErrDB.Withf("Get app[%s] stream[%s] err[%s]", app, stream, err.Error())
	}
	return out, nil
}

// CreateStreamProxy 创建拉流代理
func (c *Core) CreateStreamProxy(ctx context.Context, in *CreateStreamProxyInput) (*StreamProxy, error) {
	var out StreamProxy
	if err := copier.Copy(&out, in); err != nil {
		slog.ErrorContext(ctx, "Copy", "err", err)
	}
	out.ID = c.uniqueID.UniqueID(bz.IDPrefixRTSP)
	out.Stream = out.ID
	out.App = "live"
	if err := c.store.StreamProxy().Create(ctx, &out); err != nil {
		if orm.IsDuplicatedKey(err) {
			return nil, reason.ErrDB.WithMsg("stream 重复，请勿重复添加")
		}
		return nil, reason.ErrDB.Withf("Create err[%s]", err.Error())
	}
	return &out, nil
}

// UpdateStreamProxy 更新拉流代理
func (c *Core) UpdateStreamProxy(ctx context.Context, in *UpdateStreamProxyInput) (*StreamProxy, error) {
	out := StreamProxy{ID: in.ID}
	if err := c.store.StreamProxy().Update(ctx, &out, func(b *StreamProxy) error {
		if err := copier.Copy(b, in); err != nil {
			slog.ErrorContext(ctx, "Copy", "err", err)
		}
		return nil
	}); err != nil {
		return nil, reason.ErrDB.Withf("Update id[%v] err[%s]", in.ID, err.Error())
	}
	return &out, nil
}

// UpdateStreamProxyKey 更新 zlm 返回的 stream key
func (c *Core) UpdateStreamProxyKey(ctx context.Context, streamKey, id string) (*StreamProxy, error) {
	out := StreamProxy{ID: id}
	if err := c.store.StreamProxy().Update(ctx, &out, func(b *StreamProxy) error {
		b.StreamKey = streamKey
		return nil
	}); err != nil {
		return nil, reason.ErrDB.Withf("Update id[%v] err[%s]", id, err.Error())
	}
	return &out, nil
}

// DeleteStreamProxy 删除拉流代理
func (c *Core) DeleteStreamProxy(ctx context.Context, id string) (*StreamProxy, error) {
	out := StreamProxy{ID: id}
	if err := c.store.StreamProxy().Delete(ctx, &out); err != nil {
		return nil, reason.ErrDB.Withf("Delete id[%v] err[%s]", id, err.Error())
	}
	return &out, nil
}
