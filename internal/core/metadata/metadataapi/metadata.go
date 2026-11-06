package metadataapi

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/gowvp/owl/internal/core/metadata"
	"github.com/gowvp/owl/internal/core/metadata/stores/metadatadb"
	"github.com/ixugo/goddd/pkg/orm"
	"github.com/ixugo/goddd/pkg/reason"
	"github.com/ixugo/goddd/pkg/web"
	"gorm.io/gorm"
)

// MetadataAPI 为 http 提供通用数据持久化的业务方法
type MetadataAPI struct {
	metadataCore metadata.Core
	needInit     func() bool
}

func NewMetadataCore(db *gorm.DB) metadata.Core {
	var store metadata.Storer
	store = metadatadb.NewDB(db).AutoMigrate(orm.GetEnabledAutoMigrate())
	return metadata.NewCore(store)
}

func NewMetadataAPI(core metadata.Core) MetadataAPI {
	return MetadataAPI{metadataCore: core}
}

// WithInitializationStatus adds live initialization state without changing stored metadata.
func (a MetadataAPI) WithInitializationStatus(needInit func() bool) MetadataAPI {
	a.needInit = needInit
	return a
}

type getMetadataOutput struct {
	*metadata.Metadata
	NeedInit *bool `json:"need_init,omitempty"`
}

// metadataIDInput 元数据 ID 路径参数
type metadataIDInput struct {
	ID string `uri:"id" binding:"required"`
}

// saveMetadataInput 保存元数据的请求参数（路径 ID + 请求体）
type saveMetadataInput struct {
	ID string `uri:"id" binding:"required"`
	metadata.SaveMetadataInput
}

// RegisterMetadata 注册通用数据持久化路由
// GET 读取不鉴权：登录页需要在未认证状态下加载页面配置（login_page 等），
// 鉴权会导致 401→重定向→登录页→再请求→401 死循环。
func RegisterMetadata(g gin.IRouter, api MetadataAPI, handler ...gin.HandlerFunc) {
	g.GET("/metadatas/:id", web.WrapH(api.getMetadata))
	group := g.Group("/metadatas", handler...)
	group.POST("/:id", web.WrapH(api.saveMetadata))
}

// >>> metadata >>>>>>>>>>>>>>>>>>>>

// findMetadatas 分页查询（保留代码，当前不提供）
// func (a MetadataAPI) findMetadatas(c *gin.Context, in *metadata.FindMetadataInput) (any, error) {
// 	items, total, err := a.metadataCore.FindMetadatas(c.Request.Context(), in)
// 	return gin.H{"items": items, "total": total}, err
// }

// getMetadata 按 ID 查询数据，未找到时返回空对象而非错误，避免前端 400
func (a MetadataAPI) getMetadata(c *gin.Context, in *metadataIDInput) (*getMetadataOutput, error) {
	out, err := a.metadataCore.GetMetadata(c.Request.Context(), in.ID)
	if err != nil {
		if errors.Is(err, reason.ErrNotFound) {
			out = &metadata.Metadata{ID: in.ID}
		} else {
			return nil, err
		}
	}
	response := &getMetadataOutput{Metadata: out}
	if in.ID == "login_page" && a.needInit != nil {
		response.NeedInit = new(a.needInit())
		c.Header("Cache-Control", "no-store")
	}
	return response, nil
}

// saveMetadata 幂等保存：已存在则更新，不存在则创建
func (a MetadataAPI) saveMetadata(c *gin.Context, in *saveMetadataInput) (*metadata.Metadata, error) {
	in.CreatedBy = web.GetUsername(c)
	in.LastUpdatedBy = in.CreatedBy
	return a.metadataCore.SaveMetadata(c.Request.Context(), &in.SaveMetadataInput, in.ID)
}

// delMetadata 删除数据（保留代码，当前不提供）
// func (a MetadataAPI) delMetadata(c *gin.Context, _ *struct{}) (*metadata.Metadata, error) {
// 	metadataID := c.Param("id")
// 	return a.metadataCore.DeleteMetadata(c.Request.Context(), metadataID)
// }
