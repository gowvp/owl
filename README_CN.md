[English](./README.md) | **中文**

<p align="center">

|<img src="./docs/logo.png" alt="GoWVP Logo" width="550"/>|<img src="./docs/logo2.png" alt="GoWVP Logo" width="550"/>|
|-|-|


</p>

<p align="center">
    <a href="https://github.com/gowvp/owl/releases"><img src="https://img.shields.io/github/v/release/ixugo/goddd?include_prereleases" alt="Version"/></a>
</p>

# GB28181 视频监控平台 开箱即用

基于 GB28181-2022 标准实现的网络视频平台，同时支持 2016/2011 版本，支持 ONVIF/RTMP/RTSP 等协议，支持 yolo 检测告警。

## 在线演示平台

+ [在线演示平台 :) ](http://gowvp.golang.space:15123/)

![](./docs/demo/play.gif)



|![](./docs/phone/login.webp)|![](./docs/phone/desktop.webp)|![](./docs/phone/gb28181.webp)|![](./docs/phone/discover.webp)|
|-|-|-|-|



## 应用场景：
+ 支持浏览器无插件播放摄像头视频。
+ 支持国标设备(摄像机、平台、NVR等)设备接入
+ 支持非国标(rtsp, rtmp，直播设备等等)设备接入，充分利旧。
+ 支持跨网视频预览。
+ 支持 Docker, Docker Compose, Kubernetes 部署


## 开源库

感谢 @panjjo 大佬的开源库 [panjjo/gosip](https://github.com/panjjo/gosip)，GoWVP 的 sip 信令基于此库，出于底层封装需要，并非直接依赖该项目，而是源代码放到了 pkg 包中。

流媒体服务支持三种

+ @夏楚 [ZLMediaKit](https://github.com/ZLMediaKit/ZLMediaKit)

+ lalmax 已支持 zlm 接口，[lalmax](https://github.com/q191201771/lalmax)

+ **[lalmax-pro/streamsvr](./docs/streamsvr_product_intro_zh.md) 有 golang 企业级流媒体的需求请联系微信 [joestar2006](https://github.com/joestarzxh)(备注留言gowvp)**
  - 对环境没有要求，不需要安装任何静态库，支持跨平台编译
  - 支持特色功能定制
  - 支持 G711(G711A/G711U) 转 AAC
  - 支持 AAC 转 opus 音频转码

项目框架基于 @ixugo [goddd](https://github.com/ixugo/goddd)

商用授权请联系微信 **golangxx**，申请备注 "owl"，非授权改动请按照 GPL 协议开源前后端源代码。

## QA

> 怎么没有前端资源? 如何加载网页呢?

[点击前往下载 www.zip 压缩包](https://github.com/gowvp/gb28181_web/releases/latest)

前端资源下载(打包)后放到项目根目录，命名为 `www` 即可正常加载。

> 有没有代码相关的学习资料?

[GB/T28181 开源日记[1]：从 0 到实现 GB28181 协议的完整实践](https://juejin.cn/post/7456722441395568651)

[GB/T28181 开源日记[2]：搭建服务端，解决跨域，接口联调](https://juejin.cn/post/7456796962120417314)

[GB/T28181 开源日记[3]：使用 React 组件构建监控数据面板](https://juejin.cn/post/7457228085826764834)

[GB/T28181 开源日记[4]：使用 ESlint 辅助开发](https://juejin.cn/post/7461539078111789108)

[GB/T28181 开源日记[5]：使用 react-hook-form 完成表单](https://juejin.cn/post/7461899974198181922)

[GB/T28181 开源日记[6]：React 快速接入 jessibuca.js 播放器](https://juejin.cn/post/7462229773982351410)

[GB/T28181 开源日记[7]：实现 RTMP 鉴权与播放](https://juejin.cn/post/7463504223177261119)

[GB/T28181 开源日记[8]：国标开发速知速会](https://juejin.cn/post/7468626309699338294)

> 有没有使用资料?

**RTMP**

[RTMP 推拉流规则](https://juejin.cn/post/7463124448540934194)

[如何使用 OBS RTMP 推流到 GB/T28181平台](https://juejin.cn/post/7463350947100786739)

[海康摄像机 RTMP 推流到开源 GB/T28181 平台](https://juejin.cn/post/7468191617020313652)

[大华摄像机 RTMP 推流到开源 GB/T28181 平台](https://juejin.cn/spost/7468194672773021731)

**GB/T28181**

[GB28181 七种注册姿势](https://juejin.cn/post/7465274924899532838)

> 播放黑屏

查看「快捷桌面」 - 「zlm 右上角设置按钮」 - 「国标收流默认地址」
此地址是否能被监控设备访问到

查看「快捷桌面」 - 「zlm 右上角设置按钮」 - 「Hook IP」
zlm 能否访问到 gowvp?? docker 合并版本填写 127.0.0.1 即可，分离部署则要明确的 IP 地址

> 列表项里的通道实际有 n 个，但仅显示部分

设计如此，超过 4 个要在管理页查看，或者点击右侧的 "查看更多"

> 使用了 nginx 反向代理，返回的播放地址无法播放或不加载快照

在反向代理那里配置以下参数，其中域名根据实际的填写

proxy_set_header X-Forwarded-Host $host;

proxy_set_header X-Forwarded-Prefix "https://gowvp.com";

proxy_set_header Upgrade $http_upgrade;

proxy_set_header Connection "upgrade";

> 如何使用其它数据库?

在 configs/config.toml 配置文件中，修改 database.dsn

[推荐] sqlite 应该为本地磁盘路径，建议默认  configs/data.db

[推荐] postgres 参考格式 `postgres://postgres:123456@127.0.0.1:5432/gb28181?sslmode=disable`

mysql 参考格式 `mysql://root:123456@127.0.0.1:5432/gb28181?sslmode=disable`

postgres 和 mysql 的格式即:
`<db_type>://<username>:<password>@<ip>:<port>/<db_name>?sslmode=disable`

> 如何关闭 AI?

可以在 `configs/config.toml` 中修改 `disabledAI = true` 全局关闭 ai 检测

目前 v1.3.0 版本是 1 秒检测 1 帧，需要播放视频在右上角手动开启.

开启分析后，每路流占用约 200MB 内存 2 核

注意：开启 AI 分析后，即使没有人观看，系统也会自动保持视频流不关闭以确保 AI 持续分析。

> 为什么开启 AI 分析后，即是没人观看，流也不会自动停止

ai 分析会拉取一道流，程序会以为有人观看

> 国标设备在线，通道离线?

属于 ipc 的问题，请检查 ipc 后台注册的 平台 sip_id 和 域是否与 gowvp/owl 一致。

> 如何自定义模型?

将自己的模型存放在 configs 目录下，改名为 `owl.onnx` 或 `owl.tflite`

目前对 onnx 支持最友好

> 播放黑屏，日志提示 zlm 连接不上?

程序会自动识别是否容器中，才会主动启动 zlm，支持 docker/containerd/podman 等，如果确实没有拉起来，尝试在 compose 文件设置环境变量 NVR_STREAM=ZLM，重启容器试试。

播放黑屏也可能是 IP 配置有误，容器内 zlm 与 owl 联系用默认 127.0.0.1 即可，收流 IP 必须填写局域网 IP。

> NVR 播放黑屏? 老旧摄像头播放黑屏?

进入「监控」-「管理」，找到对应的设备，选择「编辑」-「下一步」，将收流模式修改为 **UDP**

等待 1 分钟后，重新点击通道播放看看。

> 摄像机注册上了，但是拿不到通道?

1. 进入「监控」-「接入信息」页面
2. 登录摄像机后台，检查 "server id" 是否与 「接入信息」页面的一致，不一致则修改为一致，重新注册即可


## 第三方鉴权

支持将鉴权请求转发到第三方服务，适用于已有统一认证体系的场景（如 SSO、OAuth2、企业内部账号系统等）。

**时序图**

![](./docs/auth.webp)

**使用场景**

+ 已有统一登录系统，希望 owl 复用现有会话，无需再次登录
+ 企业内网环境，需要对接 LDAP、CAS、OAuth2 等认证源
+ API 网关层已做鉴权，希望将校验结果透传到 owl

**配置方式**

在 `configs/config.toml` 中设置 `AuthURL` 为你的第三方鉴权服务地址：

```toml
[Server.HTTP]
  AuthURL = "https://your-auth-server.com/api/verify"
```

**工作原理**

配置 `AuthURL` 后，所有请求将通过第三方鉴权服务验证权限。GoWVP 会以 POST 方式将原始请求的 Header 和 Body 透传到该地址，第三方服务返回 `200` 表示鉴权通过，其它状态码则鉴权失败，响应内容直接返回给客户端。

> 注意：第三方鉴权服务需要在 10 秒内响应，否则视为超时失败。

## 文档

GoWVP [在线接口文档](https://apifox.com/apidoc/shared-7b67c918-5f72-4f64-b71d-0593d7427b93)

ZLM使用文档 [github.com/ZLMediaKit/ZLMediaKit](https://github.com/ZLMediaKit/ZLMediaKit)

// >>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>
<h1>看到这里啦，恭喜你发现新项目</h1>
<h1>点个 star 不迷路</h1>
// >>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>


## Docker

### 视频指南

[如何构建或运行项目](https://www.bilibili.com/video/BV1QLQeYHEXb)

[如何用 docker compose 部署项目](https://www.bilibili.com/video/BV112QYY3EZX)

[docker hub](https://hub.docker.com/r/gospace/gowvp)

** gowvp & zlmediakit 融合镜像(推荐)**

docker-compose.yml
```yml
services:
  gowvp:
    # 如果拉不到 docker hub 镜像，也可以尝试
    # pro: registry.cn-shanghai.aliyuncs.com/ixugo/owlpro:latest
    # 开源: registry.cn-shanghai.aliyuncs.com/ixugo/homenvr:latest
    image: gospace/gowvp:latest
    restart: unless-stopped
    # network_mode 和 ports 二选一
    # network_mode: host
    ports:
      # gb28181
      - 15123:15123 # HTTP：Web 管理 + ONVIF SOAP 服务端
      - 3702:3702/udp # ONVIF WS-Discovery 组播发现
      - 15060:15060 # gb28181 sip tcp 端口
      - 15060:15060/udp # gb28181 sip udp 端口
      # zlm
      - 1935:1935 # rtmp
      - 554:554 # rtsp
      # - 8080:80 # http
      # - 8443:443 # https
      # - 10000:10000 # rtp 单端口收流
      - 20000-20100:20000-20100 # gb28181 收流端口
      - 20000-20100:20000-20100/udp # gb28181 收流端口udp
    volumes:
      # 日志目录是 configs/logs
      - ./data:/opt/media/bin/configs
      # 录像
      - ./recordings:/opt/media/bin/configs/recordings
```

另外，gowvp 和 zlm 可以分开部署，在配置文件配置一下连接地址和端口


## 快速开始

如果你是 Go 语言开发者并熟悉 docker，可以下载源代码，本地编程运行。

**前置条件**

+ Golang
+ Docker & Docker Compose
+ Make

**操作流程**

+ 1. 克隆本项目
+ 2. 修改 configs/config.toml 中 `WebHookIP` 为你的局域网 IP
+ 3. 执行 `make build/linux && docker compose up -d`
+ 4. 自动创建了 zlm.conf 文件夹，获取 config.ini 的 api 秘钥，填写到 `configs/config.toml` 的 `Secret`
+ 5. 执行 `docker compose restart`
+ 6. 浏览器访问 `http://localhost:15123`


##  如何参与开发?

1. fork 本项目
2. 编辑器 run/debug 设置配置输出目录为项目根目录
3. 修改，提交 PR，说明修改内容

## 功能特性

**OWL**

- [x] 开箱即用，支持响应式 web 管理
- [x] 支持输出 FLV、WS-FLV、HLS、WebRTC、RTSP、RTMP 等多种协议流地址
- [x] 支持局域网/互联网/多层 NAT/特殊网络环境部署
- [x] 支持 SQLite 数据库快速部署
- [x] 支持 PostgreSQL/MySQL 数据库
- [x] 服务重启自动离线/自动尝试连接
- [x] GB/T28181
  - [x] 设备注册，支持 7 种接入方式
  - [x] 支持 UDP 和 TCP 两种国标信令传输模式
  - [x] 设备校时
  - [x] 支持 PTZ 云台控制
  - [x] 支持信息查询
    - [x] 设备目录查询
    - [x] 设备信息查询
    - [x] 设备基础配置查询(例如设备侧填写超时 3 秒，次数 3 次，则 9+x 秒左右收不到心跳认为离线，x 是检测间隔周期)
  - [x] 设备实时直播
  - [x] 支持 UDP 和 TCP 被动两种国标流传输模式
  - [x] 按需拉流，节省流量 (30秒无人观看自动停止)
  - [x] 视频支持播放 H264 和 H265
  - [x] 音频支持 g711a/g711u/aac
  - [x] 快照
  - [x] 支持跨域
- [x] 支持 onvif 接入与播放（客户端，发现外部摄像机）
- [x] 支持 onvif 虚拟设备/服务端（向 Home Assistant 等暴露平台通道）
- [x] 支持 rtmp 推流
- [x] 支持 rtsp 拉流
- [x] 支持 ai 算法分析与告警
- [x] 平台录像回放(由 owl 录制在服务器磁盘)
- [ ] 支持 ONVIF PTZ 云台控制
- [x] 支持中文和 English
- [x] SIP IP 限流，国外攻击特征系统防护，防止云服务被境外 sip 攻击

**OWL PRO**

- [x] 分屏预览
- [x] 分屏卡存录像回放(摄像头内置录像)
- [x] 录像文件下载
- [x] 设备远程重启
- [x] 主子码流
- [x] 自研 owl 播放器，兼容不同环境的 H265 流播放(仅支持 FLV 协议)

## ONVIF 虚拟设备（服务端）

GoWVP 可作为 **ONVIF 网络视频发送设备（NVT）**，供 Home Assistant 等系统按 ONVIF 摄像机方式接入，并通过 `GetStreamUri` 获取平台通道对应的 RTSP 地址。

### 端口与防火墙（局域网）

| 用途 | 协议 | 端口 | 是否要开端口 |
| --- | --- | --- | --- |
| Web 管理 + ONVIF SOAP | TCP | `Server.HTTP.Port`（默认 **15123**） | 仅当 HA/NVR 与 GoWVP 跨网段访问时需要；同网段一般不用单独放行 |
| `GetStreamUri` 返回的 RTSP 拉流 | TCP | ZLM RTSP（默认 **554**，见 `docker-compose` 映射） | 客户端从 ZLM 拉流时，跨网段需能访问该端口 |
| ONVIF WS-Discovery（本机对外广播） | UDP | **3702**（组播 `239.255.255.250`） | Docker 需映射 `3702:3702/udp`；同网段一般无需单独防火 |

ONVIF SOAP 路径（与 Web 共用 HTTP 端口）：

- `POST http://<主机>:15123/onvif/device_service`
- `POST http://<host>:15123/onvif/media_service`

鉴权使用 `configs/config.toml` 中的 `Server.Username` / `Server.Password`（WS-Security），与 Web 登录默认一致（`admin` / `admin`）。

**Home Assistant 手动添加示例：**

- 主机：`http://<局域网IP>:15123/onvif/device_service`
- 用户名 / 密码：与 `config.toml` 中 `Server.Username`、`Server.Password` 一致

**自动发现：** 启动后在本机 **UDP 3702** 应答 WS-Discovery Probe，HA 可搜索到设备；`configs/config.toml` 中 `Media.IP`（或 `Sip.Host`）会写入 `XAddrs`，宜填局域网 IP。

**说明：** `GET /onvif/discover` 是 GoWVP **扫描局域网内其它 ONVIF 摄像机**（客户端能力），与上述服务端组播发现不是同一功能。

## Webhook 告警事件推送与接收

GoWVP 支持将告警事件通过 HTTP Webhook 推送到外部系统，也支持接收来自其他 GoWVP 实例的告警推送，实现**主从级联**部署。

### 路由

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/webhook/events` | 统一事件接收入口，兼容 Python AI 和 gowvp 间转发两种来源 |

### 配置（`configs/config.toml`）

```toml
[Server.Webhook]
  # 推送目标 URL 数组，secret 直接内嵌于 URL query 参数
  Targets = [
    "http://192.168.1.100:15123/webhook/events?secret=your-recv-secret",
  ]
  # 最大重试次数，0 = 使用内置默认值 3
  MaxRetry = 3
  # 每个目标的 channel 缓冲队列大小，0 = 内置默认 64
  BufferSize = 64
  # 本节点接收 webhook 时校验的密钥（首次启动自动生成并持久化）
  RecvSecret = "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
```

### 推送 payload 格式（gowvp → gowvp）

```json
{
  "did": "device-id",
  "cid": "channel-id",
  "started_at": "2024-01-01T10:00:00Z",
  "ended_at": "2024-01-01T10:00:01Z",
  "label": "person",
  "score": 0.95,
  "zones": "{\"x_min\":0,\"y_min\":0,\"x_max\":100,\"y_max\":100}",
  "image_base64": "<base64 编码的 JPEG 图片>",
  "model": "yolov8"
}
```

> `image_base64`：主节点读取本地图片后 base64 编码随 payload 发送，副节点解码后落盘存储，解决跨节点文件路径失效问题。
> 若图片读取失败，`image_base64` 字段为空，副节点不存图片但事件数据仍会入库。

### 重试策略

推送失败时自动重试，采用**指数退避 + ±25% jitter**：

| 重试次 | 基础延迟 |
|--------|----------|
| 第 1 次 | ~1s |
| 第 2 次 | ~2s |
| 第 3 次 | ~4s（上限 10s）|

HTTP 4xx（除 429/408）视为永久失败，不再重试。每次重试失败记录 warn 日志，耗尽重试次数记录 error 日志。

## 感谢

感谢赞助，排名不分先后。

[@joestarzxh](https://github.com/joestarzxh)
[@oldweipro](https://github.com/oldweipro)
[@beixiaocai](https://github.com/beixiaocai)
[@chencanfggz](https://github.com/chencanfggz)
[@zhangxuan1340](https://github.com/zhangxuan1340)


## 许可证

本项目采用 **[GNU 通用公共许可证 v3.0 (GPL-3.0)](https://www.gnu.org/licenses/gpl-3.0.html)** 授权。
  - **您可以自由使用、修改和分发本项目的代码**，但必须遵循以下条件：
  - **开源要求**：任何基于本项目的衍生作品（包括修改后的代码或集成本项目的软件）**必须同样以 GPL-3.0 协议开源**。
  - **保留协议与版权声明**：在衍生作品中需包含原项目的 `LICENSE` 文件及原始版权声明。
  - **明确修改说明**：若您修改了代码，需在文件中注明变更内容。

⚠ **注意**：若将本项目用于商业闭源软件或 SaaS 服务，需遵守 GPL-3.0 的传染性条款（即相关代码必须开源）。

完整许可证文本请见 [LICENSE](./LICENSE) 文件。
