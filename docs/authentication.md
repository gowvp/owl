# GoWVP 开发者鉴权方案指南

本文档面向系统集成开发者与前端开发者，详细介绍 GoWVP（OWL）提供的三种接口鉴权机制、设计原理、配置方式及调用示例。

---

## 1. 鉴权方案概览

GoWVP 支持以下三种鉴权模式，三种模式在网关中间件中无缝协同、层层兜底：

| 鉴权方案 | 凭据形态 | 时效性 | 权限等级 | 核心适用场景 |
| :--- | :--- | :--- | :--- | :--- |
| **APISecret 静态秘钥** | 固定字符串 | 永久有效（直至配置变更） | 默认管理员（admin） | 外部系统集成、自动化脚本、后台监控、长期微服务通信 |
| **JWT 动态令牌** | 三段式 Base64 签名串 | 动态过期（默认3天） | 按登录身份签发 | Web 前端用户登录、管理控制台、交互式客户端 |
| **第三方 AuthURL** | 透传原始 Header/Body | 由第三方服务判定 | 由第三方服务控制 | 企业已有 SSO 单点登录、统一认证中心、集中权限网关 |

---

## 2. 鉴权流程与决策链路

当客户端向受保护接口发起请求时，中间件遵循**“静态优先、动态验签、远程兜底”**的过滤机制：

```text
客户端请求 (Header: Authorization / Query: token)
                    │
                    ▼
          提取 tokenStr (剥离可选 Bearer 前缀)
                    │
                    ▼
     [配置了 APISecret 且凭据完全匹配?]
           ├── 是 ──► 注入管理员上下文 (username=admin, role=admin) ──► 放行 (200)
           └── 否
                    │
                    ▼
     [凭据为 Bearer 前缀且 JWT 解析成功?]
           ├── 成功 ──► 注入 JWT Claims ──► 放行 (200)
           ├── 过期 ──► 直接拦截 (401 Token Expired, 要求重新登录)
           └── 失败
                    │
                    ▼
     [是否配置了第三方 AuthURL?]
           ├── 是 ──► 原样透传 Header 与 Body 至远程鉴权服务
           │           ├── 远程返回 200 ──► 放行
           │           └── 远程返回非 200 ──► 原样透传远程响应状态与内容
           └── 否 ──► 拦截 (401 身份验证失败)
```

### 关键设计优势

1. **零性能损耗**：APISecret 采用 `crypto/subtle.ConstantTimeCompare` 常数时间比对，既规避了时序侧信道攻击，又无需执行耗时的 Base64 解码与数字验签，高频 API 调用耗时仅纳秒级。
2. **长短互补**：日常开发管理使用 JWT 保证会话时效安全性；后端脚本或跨节点同步使用 APISecret，避免定时轮询刷新 Token 的复杂心跳逻辑。
3. **接口调用一致性**：无论使用哪种鉴权方式，均支持标准的 HTTP `Authorization: Bearer <token>` 头部，调用方无须更改请求头封装。

---

## 3. 方案详解与调用示例

### 方案一：APISecret 永久秘钥鉴权

适用于自动化巡检、Prometheus 指标拉取、流媒体节点纳管、外部业务系统服务端调用。

#### 1. 规约与格式
- 密钥仅允许由 **数字、大小写字母、下划线** 组成。
- 长度限制：**1 ~ 32 位**。
- 系统在每次启动时会自动校验此配置项：
  - 若配置为空，系统将自动生成 32 位无连字符 UUID，并持久化回写至 `config.toml`；
  - 若配置了非法字符或超长，系统会打印 WARN 警告日志，并自动重新生成合规的 32 位秘钥覆盖持久化。

#### 2. 配置文件 (`configs/config.toml`)
```toml
[Server.HTTP]
  # 永久 API 秘钥，替代 JWT 鉴权
  APISecret = 'my_secure_api_secret_2026'
```

#### 3. 调用方式
支持两种传参途径：
- **Header 方式（推荐）**：
  ```bash
  curl -H "Authorization: Bearer my_secure_api_secret_2026" http://127.0.0.1:15123/debug/sip/memory
  ```
- **URL Query 方式**（适合浏览器直连、播放器拉流或轻量 Webhook）：
  ```bash
  curl "http://127.0.0.1:15123/debug/sip/memory?token=my_secure_api_secret_2026"
  ```

---

### 方案二：JWT 动态令牌鉴权

适用于 Web 管理控制台、App 客户端等需要用户登录交互的场景。

#### 1. 获取令牌
调用系统登录接口获取 JWT Token：
```bash
curl -X POST http://127.0.0.1:15123/api/v1/user/login \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "your_password"}'
```
响应体示例：
```json
{
  "reason": "OK",
  "msg": "success",
  "details": [],
  "trace_id": "0191e4b3-a123-74b8-8c12-3456789abcde",
  "data": {
    "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
    "user": "admin"
  }
}
```

#### 2. 调用受保护接口
在请求头中携带 Token：
```bash
curl -H "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..." http://127.0.0.1:15123/app/network/subnets
```

#### 3. 过期与鉴权失败处理
当 Token 超过签发有效期（默认 3 天）时，接口会直接返回 401 统一错误响应：
```json
{
  "reason": "ErrUnauthorizedToken",
  "msg": "请重新登录",
  "details": [],
  "trace_id": "0191e4b3-a123-74b8-8c12-3456789abcde"
}
```
若未传递凭证或凭据非法且无第三方鉴权服务兜底时，接口返回：
```json
{
  "reason": "ErrUnauthorizedToken",
  "msg": "身份验证失败",
  "details": [],
  "trace_id": "0191e4b3-a123-74b8-8c12-3456789abcde"
}
```
此时客户端需重新发起登录换取新 Token。

---

### 方案三：第三方 AuthURL 转发鉴权

适用于企业内部已有统一账号与权限体系（如 OAuth2、CAS、统一网关服务），希望将 GoWVP 的鉴权委托给自研鉴权服务。

#### 1. 配置文件 (`configs/config.toml`)
```toml
[Server.HTTP]
  # 填写第三方鉴权服务的 HTTP 地址，留空表示不启用
  AuthURL = 'http://auth.internal.company.com/verify'
```

#### 2. 工作原理
1. 当客户端发起的请求未命中 APISecret，且未通过系统原生 JWT 验签时，中间件将原始 HTTP 请求的所有 Header 与 Body 原样 POST 转发给 `AuthURL`。
2. 规则裁决：
   - 若第三方鉴权服务返回 HTTP 状态码 **`200 OK`**：GoWVP 判定鉴权通过，继续执行接口逻辑；
   - 若第三方服务返回其他状态码（如 401、403、500 等）：GoWVP 直接将该响应状态码和响应体原样输出给客户端。

---

## 4. 多语言代码集成示例

### Python
```python
import requests

# 方式一：使用 APISecret
headers = {
    "Authorization": "Bearer my_secure_api_secret_2026"
}
resp = requests.get("http://127.0.0.1:15123/app/network/subnets", headers=headers)
print(resp.json())

# 方式二：URL Query 传参
resp_query = requests.get("http://127.0.0.1:15123/app/network/subnets?token=my_secure_api_secret_2026")
print(resp_query.json())
```

### Go
```go
package main

import (
	"fmt"
	"io"
	"net/http"
)

func main() {
	client := &http.Client{}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:15123/app/network/subnets", nil)
	req.Header.Set("Authorization", "Bearer my_secure_api_secret_2026")

	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Println(string(body))
}
```

### JavaScript / TypeScript (Fetch)
```javascript
const API_URL = "http://127.0.0.1:15123/app/network/subnets";
const API_SECRET = "my_secure_api_secret_2026";

async function fetchSubnets() {
  const response = await fetch(API_URL, {
    method: "GET",
    headers: {
      "Authorization": `Bearer ${API_SECRET}`
    }
  });
  const data = await response.json();
  console.log(data);
}
```

---

## 5. 安全最佳实践

1. **生产环境秘钥保护**：APISecret 具备全站最高管理员权限，且永不过期，严禁提交到开源仓库或前端代码中。
2. **结合 HTTPS / 反向代理**：建议在生产环境中通过 Nginx 启用 HTTPS，避免明文 HTTP 传输导致网络窃听。
3. **内外网隔离**：若对外暴露接口，建议限制 APISecret 仅在受信任的内网 IP 段使用，或在反向代理层限制特定路径。
