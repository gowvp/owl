
**English** | [中文](./README_CN.md)

<p align="center">

 |<img src="./docs/logo.png" alt="GoWVP Logo" width="550"/>|<img src="./docs/logo2.png" alt="GoWVP Logo" width="550"/>|
|-|-|
</p>

<p align="center">
    <a href="https://github.com/gowvp/owl/releases"><img src="https://img.shields.io/github/v/release/ixugo/goddd?include_prereleases" alt="Version"/></a>
</p>

# Out-of-the-Box Video Surveillance Platform

GoWVP is an open-source GB28181 solution implemented in Go, a network video platform based on the GB28181-2022 standard that also supports 2016/2011 versions, with ONVIF/RTMP/RTSP protocol support.

## Live Demo

+ [Online Demo Platform :)](http://gowvp.golang.space:15123/)

![](./docs/demo/play.gif)

|![](./docs/phone/login.webp)|![](./docs/phone/desktop.webp)|![](./docs/phone/gb28181.webp)|![](./docs/phone/discover.webp)|
|-|-|-|-|

## Use Cases

+ Browser-based camera video playback without plugins
+ Support for GB28181-compliant devices (IP cameras, platforms, NVRs, etc.)
+ Support for non-GB28181 devices (RTSP, RTMP, streaming devices, etc.) - maximize your existing equipment
+ Cross-network video preview
+ Deployment via Docker, Docker Compose, or Kubernetes

## On-demand preview transcoding

WebRTC previews on the default ZLMediaKit node can transcode on demand. After validating the original channel's play token, Owl checks the browser SDP and source codec. FFmpeg starts only for an H.265 source when the browser offers H.264 without H.265, producing H.264 Baseline video and Opus audio. H.264 sources and H.265-capable browsers use the original stream. No frontend or camera codec changes are required.

Viewers of the same camera share one process. Owl checks ZLM's actual reader count every 5 seconds and stops/reaps the process after approximately 30 seconds without viewers, including failed ICE connections and abandoned requests. Startup failures release their slots, and graceful Owl shutdown stops all transcoders. Initial playback waits for the source and transcoder to become ready; retry if the browser times out early.

Derived streams use the reserved `owl_preview` app with a per-job internal key. Do not use this app for camera channels. These streams are excluded from recording and original-channel recording/AI callbacks; original recordings retain the camera's codec.

Add the following options to the existing `[Media]` section of `configs/config.toml`, then restart Owl:

```toml
PreviewDisabled = false
PreviewFFmpeg = 'ffmpeg'
PreviewIdleSeconds = 30
PreviewMaxConcurrent = 3
```

Omitted fields enable the feature with the same defaults. An empty binary path resolves `ffmpeg` from `PATH`; zero idle/concurrency settings mean 30 seconds/3 cameras. FFmpeg must provide `libx264` and `libopus`, and Owl must reach ZLM's RTSP and HTTP API ports. The existing ZLM Docker image includes FFmpeg; standalone binaries need it installed separately.

Transcoding uses software encoding with up to 2 encoder threads per camera. The concurrency limit counts cameras, not viewers; adjust it to the server's CPU capacity and source resolution/frame rate. This feature handles live WebRTC previews on the default ZLM node; other protocols, recordings, Lalmax and remote nodes retain existing behavior.

## Open Source Libraries

Thanks to @panjjo for the open-source library [panjjo/gosip](https://github.com/panjjo/gosip). GoWVP's SIP signaling is based on this library. Due to underlying encapsulation requirements, it's not a direct dependency but rather included in the pkg package.

Two streaming media servers are supported:

+ @夏楚 [ZLMediaKit](https://github.com/ZLMediaKit/ZLMediaKit)

+ **lalmax-pro** - For Go streaming media needs, contact email xx@golang.space
  - No environment requirements, no static library installation needed, cross-platform compilation support
  - Custom feature development available
  - G711 (G711A/G711U) to AAC transcoding support

Project framework based on @ixugo's [goddd](https://github.com/ixugo/goddd)

For commercial licensing, please contact WeChat: **golangxx**. Unauthorized modifications must open-source both frontend and backend source code under the GPL license.

## FAQ

> Where are the frontend resources? How to load the web interface?

[Click to download www.zip package](https://github.com/gowvp/gb28181_web/releases/latest)

Download (packaged) frontend resources and place them in the project root directory, named `www` to load properly.

> Any learning materials about the code?

[GB/T28181 Open Source Diary[1]: Complete Practice from 0 to Implementing GB28181 Protocol](https://juejin.cn/post/7456722441395568651)

[GB/T28181 Open Source Diary[2]: Setting Up Server, Solving CORS, API Integration](https://juejin.cn/post/7456796962120417314)

[GB/T28181 Open Source Diary[3]: Building Monitoring Dashboard with React Components](https://juejin.cn/post/7457228085826764834)

[GB/T28181 Open Source Diary[4]: Using ESLint for Development](https://juejin.cn/post/7461539078111789108)

[GB/T28181 Open Source Diary[5]: Completing Forms with react-hook-form](https://juejin.cn/post/7461899974198181922)

[GB/T28181 Open Source Diary[6]: Quick Integration of jessibuca.js Player in React](https://juejin.cn/post/7462229773982351410)

[GB/T28181 Open Source Diary[7]: Implementing RTMP Authentication and Playback](https://juejin.cn/post/7463504223177261119)

[GB/T28181 Open Source Diary[8]: Quick Guide to GB28181 Development](https://juejin.cn/post/7468626309699338294)

> Any usage documentation?

**RTMP**

[RTMP Push/Pull Stream Rules](https://juejin.cn/post/7463124448540934194)

[How to Use OBS RTMP Push Stream to GB/T28181 Platform](https://juejin.cn/post/7463350947100786739)

[Hikvision Camera RTMP Push Stream to Open Source GB/T28181 Platform](https://juejin.cn/post/7468191617020313652)

[Dahua Camera RTMP Push Stream to Open Source GB/T28181 Platform](https://juejin.cn/spost/7468194672773021731)

**GB/T28181**

[7 Ways to Register GB28181 Devices](https://juejin.cn/post/7465274924899532838)

> Black screen when playing

Check "Quick Desktop" - "ZLM settings button (top right)" - "GB28181 stream receiving default address"
Ensure this address is accessible by the surveillance device.

Check "Quick Desktop" - "ZLM settings button (top right)" - "Hook IP"
Can ZLM access GoWVP? For Docker combined version, use 127.0.0.1. For separate deployment, use explicit IP address.

> Channel list shows fewer channels than actual count

By design. More than 4 channels should be viewed in the management page, or click "View More" on the right.

> Using nginx reverse proxy, returned playback addresses don't work or snapshots don't load

Configure the following parameters in reverse proxy (replace domain with your actual one):

proxy_set_header X-Forwarded-Host $host;

proxy_set_header X-Forwarded-Prefix "https://gowvp.com";

proxy_set_header Upgrade $http_upgrade;

proxy_set_header Connection "upgrade";

> How to use other databases?

In the `configs/config.toml` configuration file, modify `database.dsn`

[Recommended] SQLite should be a local disk path, default is `configs/data.db`

[Recommended] PostgreSQL format: `postgres://postgres:123456@127.0.0.1:5432/gb28181?sslmode=disable`

MySQL format: `mysql://root:123456@127.0.0.1:5432/gb28181?sslmode=disable`

PostgreSQL and MySQL format pattern:
`<db_type>://<username>:<password>@<ip>:<port>/<db_name>?sslmode=disable`

> How to disable AI?

AI detection is enabled by default, detecting 5 frames per second.

You can disable AI detection by setting `disabledAI = true` in `configs/config.toml`

## Third-Party Authentication

GoWVP supports forwarding authentication requests to a third-party service, ideal for environments with an existing unified authentication system (SSO, OAuth2, corporate account systems, etc.).

**Sequence Diagram**

![](./docs/auth.webp)

**Use Cases**

+ Your organization has a unified login system and you want GoWVP to reuse existing sessions without requiring users to log in again
+ Enterprise intranet environments that need to integrate with LDAP, CAS, OAuth2, or other authentication providers
+ API gateway handles authentication and you want to pass the verification result through to GoWVP

**Configuration**

Set `AuthURL` to your third-party authentication service address in `configs/config.toml`:

```toml
[Server.HTTP]
  AuthURL = "https://your-auth-server.com/api/verify"
```

**How It Works**

Once `AuthURL` is configured, all requests are verified through the third-party authentication service. GoWVP forwards the original request headers and body as a POST to the specified URL. A `200` response means authentication passes; any other status code means failure, and the response body is returned directly to the client.

> Note: The third-party auth service must respond within 10 seconds, otherwise the request is considered timed out.

## Documentation

GoWVP [Online API Documentation](https://apifox.com/apidoc/shared-7b67c918-5f72-4f64-b71d-0593d7427b93)

ZLM Documentation [github.com/ZLMediaKit/ZLMediaKit](https://github.com/ZLMediaKit/ZLMediaKit)

// >>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>
<h1>You've made it this far!</h1>
<h1>Give us a ⭐ star!</h1>
// >>>>>>>>>>>>>>>>>>>>>>>>>>>>>>>

## Docker

### Video Guide

[How to Build or Run the Project](https://www.bilibili.com/video/BV1QLQeYHEXb)

[How to Deploy with Docker Compose](https://www.bilibili.com/video/BV112QYY3EZX)

[Docker Hub](https://hub.docker.com/r/gospace/gowvp)

**GoWVP & ZLMediaKit Combined Image (Recommended)**

docker-compose.yml

```yml
services:
  gowvp:
    # If Docker Hub image is unavailable, try:
    # pro: registry.cn-shanghai.aliyuncs.com/ixugo/owlpro:latest
    # 开源: registry.cn-shanghai.aliyuncs.com/ixugo/homenvr:latest
    image: gospace/gowvp:latest
    restart: unless-stopped
    # For Linux, uncomment the line below and comment out all ports
    # network_mode: host
    ports:
      # gb28181
      - 15123:15123 # HTTP: Web UI + ONVIF SOAP server
      - 3702:3702/udp # ONVIF WS-Discovery (能够被其它内网 onvif 发现本平台)
      - 15060:15060 # GB28181 SIP TCP port
      - 15060:15060/udp # GB28181 SIP UDP port
      # zlm
      - 1935:1935 # rtmp
      - 554:554 # rtsp
      # - 8080:80 # http
      # - 8443:443 # https
      # - 10000:10000
      - 20000-20100:20000-20100 # GB28181 stream receiving ports
      - 20000-20100:20000-20100/udp # GB28181 stream receiving UDP ports
    volumes:
      # Log directory is configs/logs
      - ./data:/opt/media/bin/configs
```


## Quick Start

If you're a Go developer familiar with Docker, you can download the source code and run locally.

**Prerequisites**

+ Golang
+ Docker & Docker Compose
+ Make

**Steps**

1. Clone this repository
2. Modify `WebHookIP` in `configs/config.toml` to your LAN IP
3. Run `make build/linux && docker compose up -d`
4. A `zlm.conf` folder is auto-created. Get the API secret from `config.ini` and fill it in `configs/config.toml` under `Secret`
5. Run `docker compose restart`
6. Access `http://localhost:15123` in your browser

## How to Contribute?

1. Fork this project
2. Set your editor's run/debug output directory to the project root
3. Make changes, submit a PR with description of modifications

## Features

- [x] Out-of-the-box with responsive web management
- [x] Multiple protocol output: FLV, WS-FLV, HLS, WebRTC, RTSP, RTMP
- [x] LAN/Internet/Multi-layer NAT/Special network environment deployment
- [x] SQLite database for quick deployment
- [x] PostgreSQL/MySQL database support
- [x] Auto offline/reconnect on service restart
- [x] GB/T 28181
  - [x] Device registration with 7 connection methods
  - [x] UDP and TCP signaling transport modes
  - [x] Device time synchronization
  - [x] PTZ control support
  - [x] Information queries support
    - [x] Device catalog query
    - [x] Device info query
    - [x] Device basic config query (e.g., timeout 3s × 3 retries = ~9+x seconds for offline detection)
  - [x] Live streaming from devices
  - [x] UDP and TCP passive stream transport modes
  - [x] On-demand streaming to save bandwidth (auto-stop after 30s without viewers)
  - [x] H264 and H265 video codec support
  - [x] g711a/g711u/aac audio codec support
  - [x] Snapshots
  - [x] CORS support
- [x] ONVIF device access and playback (client)
- [x] ONVIF virtual device / server (expose channels to Home Assistant, etc.)
- [x] RTMP push streaming support
- [x] RTSP pull streaming support
- [x] AI algorithm analysis and alerting support
- [x] Cloud Recording playback (owl)
- [ ] ONVIF PTZ control support
- [x] Chinese and English language support

**OWL PRO**
- [x] Multi-screen split preview
- [x] Multi-screen SD card recording playback (camera built-in recording)
- [x] Recording file download
- [x] Remote device reboot
- [x] Main and sub stream switching
- [x] Proprietary owl player with broad H265 stream playback compatibility (FLV only)

## ONVIF Virtual Device (Server)

GoWVP can act as an **ONVIF Network Video Transmitter** so integrators (e.g. Home Assistant) can add it like a camera and pull RTSP URLs for platform channels.

### Ports and firewall (LAN)

| Purpose | Protocol | Port | Open in firewall? |
| --- | --- | --- | --- |
| Web UI + ONVIF SOAP | TCP | `Server.HTTP.Port` (default **15123**) | Only if HA/NVR is on another subnet; same LAN usually needs no extra rule |
| RTSP playback after `GetStreamUri` | TCP | ZLM RTSP (default **554**, see `docker-compose` mapping) | Same as above when the client pulls stream from ZLM |
| ONVIF WS-Discovery (server advertisement) | UDP | **3702** (multicast `239.255.255.250`) | Map `3702:3702/udp` in Docker; LAN usually needs no extra rule |

ONVIF SOAP endpoints (same HTTP port as the web UI):

- `POST http://<host>:15123/onvif/device_service`
- `POST http://<host>:15123/onvif/media_service`

Authentication uses `Server.Username` / `Server.Password` in `configs/config.toml` (WS-Security `PasswordDigest` or `PasswordText`), same defaults as web login (`admin` / `admin`).

**Home Assistant (manual add):**

- Host: `http://<lan-ip>:15123/onvif/device_service`
- Username / password: values from `config.toml` `Server.Username` / `Server.Password`

**Auto-discovery:** After startup, GoWVP answers WS-Discovery Probe on **UDP 3702**. Set `Media.IP` (or `Sip.Host`) in `configs/config.toml` to your LAN IP so `XAddrs` is reachable from Home Assistant.

**Not the same as** `GET /onvif/discover`: that API lets GoWVP **scan the LAN for external ONVIF cameras** (client role).

## Webhook Alert Push & Receive

GoWVP supports pushing alert events to external systems via HTTP Webhook, and receiving pushes from other GoWVP instances for **master-slave cascading** deployments.

### Endpoint

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/webhook/events` | Unified event receiver, compatible with both Python AI and gowvp-to-gowvp forwarding |

### Configuration (`configs/config.toml`)

```toml
[Server.Webhook]
  # Target URL array; embed the secret as a query parameter
  Targets = [
    "http://192.168.1.100:15123/webhook/events?secret=your-recv-secret",
  ]
  # Max retries, 0 = built-in default of 3
  MaxRetry = 3
  # Channel buffer size per target, 0 = built-in default of 64
  BufferSize = 64
  # Secret for validating incoming webhook requests (auto-generated on first launch)
  RecvSecret = "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
```

### Authentication

`/webhook/events` accepts two authentication methods (either one suffices):

| Source | Method | Notes |
|--------|--------|-------|
| Python AI | `Authorization: Basic <InternalSecret>` header | Random UUID generated at startup, not persisted |
| Other gowvp | URL query `?secret=<RecvSecret>` | Configured in `RecvSecret`, auto-generated on first launch |

### Master-Slave Cascading

**Master node**: Receives AI detection events and pushes alerts to slave nodes.

```toml
# Master node config.toml
[Server.Webhook]
  Targets = ["http://<slave-ip>:15123/webhook/events?secret=<slave-RecvSecret>"]
```

**Slave node**: Receives pushes and stores events directly (device/channel existence is not validated).

```toml
# Slave node config.toml (RecvSecret is auto-generated on first launch)
[Server.Webhook]
  RecvSecret = "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
```

### Forward Payload Format (gowvp → gowvp)

```json
{
  "did": "device-id",
  "cid": "channel-id",
  "started_at": "2024-01-01T10:00:00Z",
  "ended_at": "2024-01-01T10:00:01Z",
  "label": "person",
  "score": 0.95,
  "zones": "{\"x_min\":0,\"y_min\":0,\"x_max\":100,\"y_max\":100}",
  "image_base64": "<base64-encoded JPEG image>",
  "model": "yolov8"
}
```

> `image_base64`: The master node reads the local image file and encodes it as base64 for transmission. The slave node decodes and saves it locally, resolving cross-node file path issues.
> If the image cannot be read, `image_base64` is omitted; the event is still saved without an image.

### Retry Strategy

On failure, the push is retried with **exponential backoff ± 25% jitter**:

| Attempt | Base Delay |
|---------|------------|
| 1st | ~1s |
| 2nd | ~2s |
| 3rd | ~4s (max 10s) |

HTTP 4xx responses (except 429/408) are treated as permanent failures and not retried. Each retry failure is logged as `warn`; exhausted retries are logged as `error`.

## Acknowledgments

Thanks to our sponsors (in no particular order):

[@joestarzxh](https://github.com/joestarzxh)
[@oldweipro](https://github.com/oldweipro)
[@beixiaocai](https://github.com/beixiaocai)
[@chencanfggz](https://github.com/chencanfggz)
[@zhangxuan1340](https://github.com/zhangxuan1340)

## License

This project is licensed under the **[GNU General Public License v3.0 (GPL-3.0)](https://www.gnu.org/licenses/gpl-3.0.html)**.

- **You are free to use, modify, and distribute** the code of this project, subject to the following conditions:
- **Open Source Requirement**: Any derivative works based on this project (including modified code or software integrating this project) **must also be open-sourced under GPL-3.0**.
- **Retain License & Copyright Notice**: Derivative works must include the original project's `LICENSE` file and copyright notices.
- **Document Modifications**: If you modify the code, you must indicate the changes in the files.

⚠ **Note**: If using this project for commercial closed-source software or SaaS services, you must comply with GPL-3.0's copyleft provisions (i.e., related code must be open-sourced).

For the complete license text, see the [LICENSE](./LICENSE) file.
