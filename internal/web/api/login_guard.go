package api

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ixugo/goddd/pkg/reason"
	"github.com/ixugo/goddd/pkg/web"
)

// loginFailureThreshold 是首次锁定前允许的最大失败次数。
const loginFailureThreshold = 5

// loginKeyRequestsPerMinute 是所有来源共享的密钥接口滚动一分钟配额。
const loginKeyRequestsPerMinute = 5

// loginInitialLock 是首次达到失败阈值后的停用时间。
const loginInitialLock = time.Minute

// loginSecondLock 是第一次解锁后再次失败的停用时间。
const loginSecondLock = 5 * time.Minute

// loginThirdLock 是第二次解锁后再次失败的停用时间。
const loginThirdLock = time.Hour

// loginMaximumLock 是后续失败的停用上限。
const loginMaximumLock = 8 * time.Hour

// loginKeyWindowDuration 是密钥请求配额的滚动统计周期。
const loginKeyWindowDuration = time.Minute

// loginGuard 只记录当前配置账号的状态，避免伪造用户名造成内存增长。
type loginGuard struct {
	mu       sync.Mutex
	failures int
	level    int
	until    time.Time
	now      func() time.Time
}

// middleware 串行核验登录结果，保证并发失败也无法越过五次阈值；每次操作为 O(1)。
func (g *loginGuard) middleware(c *gin.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now := g.now(); now.Before(g.until) {
		abortLoginLimit(c, g.until.Sub(now))
		return
	}
	c.Next()
	if c.Writer.Status() >= http.StatusOK && c.Writer.Status() < http.StatusMultipleChoices {
		g.failures, g.level, g.until = 0, 0, time.Time{}
		return
	}
	g.failures = min(g.failures+1, loginFailureThreshold)
	if g.failures < loginFailureThreshold {
		return
	}
	durations := [...]time.Duration{loginInitialLock, loginSecondLock, loginThirdLock, loginMaximumLock}
	g.until = g.now().Add(durations[g.level])
	g.level = min(g.level+1, len(durations)-1)
}

// abortLoginLimit 保持项目错误响应格式，并用 Retry-After 告知最早重试时间。
func abortLoginLimit(c *gin.Context, remaining time.Duration) {
	seconds := int64((remaining + time.Second - 1) / time.Second)
	c.Header("Retry-After", strconv.FormatInt(seconds, 10))
	web.AbortWithStatusJSON(c, reason.ErrTooManyRequests.WithMsg(fmt.Sprintf("登录请求受限，请在 %d 秒后重试", seconds)))
}

// newLoginKeyWindow 用固定五个时间戳补足令牌桶的突发语义，严格限制滚动一分钟；操作为 O(1)。
func newLoginKeyWindow(now func() time.Time) gin.HandlerFunc {
	var mu sync.Mutex
	var accepted [loginKeyRequestsPerMinute]time.Time
	var next int
	return func(c *gin.Context) {
		mu.Lock()
		current := now()
		if remaining := accepted[next].Add(loginKeyWindowDuration).Sub(current); remaining > 0 {
			mu.Unlock()
			abortLoginLimit(c, remaining)
			return
		}
		accepted[next] = current
		next = (next + 1) % len(accepted)
		mu.Unlock()
		c.Next()
	}
}
