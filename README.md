# IPADownStudio

一个基于 Go 1.24 标准库与现代 Web 桌面界面的 App Store 历史版本 IPA 下载与管理工具，使用你自己的 Apple 账号浏览历史版本、管理下载队列与断点续传，并通过局域网二维码将 IPA 传输到移动设备。

> A lightweight local Web-Desktop app built in pure Go 1.24 (zero third-party Go module dependencies) to search the App Store, browse historical app versions, download IPA packages with your own Apple ID, and share files over LAN via QR code.

## 核心特性 / Features

- **多账号管理与 2FA**：通过独立 `IPATOOL_PROFILE_ID` 与 `IPATOOL_STATE_DIR` 隔离不同 Apple ID 会话；使用 `--credentials-stdin` 传递凭据，密码与双重认证验证码仅保留在内存中用于当次验证，绝不写入磁盘配置文件。
- **App Store 搜索与已购列表**：支持应用名称、App Store ID（如 `414478124`）、Bundle ID（如 `com.tencent.xin`）及 `apps.apple.com` 商店链接；支持切换 CN/US/JP/HK/TW 等多个商店地区与 iPhone/iPad/macOS/tvOS 平台，并可查看当前账号的已购应用列表。
- **历史版本查询与详情缓存**：列出全部可用历史版本 ID（倒序显示最新版本在前），分批并发拉取并本地缓存版本号、发布日期、文件大小与最低 iOS 版本；支持直接输入已知 `External Version ID` 下载指定老版本及一键获取免费应用许可。
- **下载队列与断点续传**：支持多任务并发（1–3 个）、实时速度与剩余时间显示、暂停保留 `.tmp` 临时文件以便断点续传、取消清理与完成后 ZIP/IPA 完整性校验。
- **局域网扫码传手机**：自动枚举局域网 IPv4 网卡并启动本地 HTTP 传输服务（支持 HTTP `Range` 断点续传与下载记录），内置纯 Go 实现的 ISO/IEC 18004 SVG 二维码生成器。
- **双语与主题切换**：内置简体中文 / English 实时切换，支持浅色 / 深色 / 跟随系统主题。

## 依赖说明 / External Dependency

本项目本身为纯 Go 标准库实现，**仓库中不包含任何 Apple 二进制文件、`ipatool` 二进制文件或 `libunicorn.dll`**。

使用前请先从开源项目 [majd/ipatool](https://github.com/majd/ipatool) 获取 `ipatool`（建议 `v2.7.0+`），并采用以下任一方式配置：

1. 将 `ipatool.exe`（及其运行所需的 `libunicorn.dll`，如所用版本需要）放入程序同级 `Tools/` 目录（或 `..\current\Tools\`）；
2. 将 `ipatool` 加入系统 `PATH` 或设置环境变量 `IPATOOL_PATH`；
3. 或在程序界面 **设置与诊断** 中手动指定 `ipatool.exe` 的路径。

## 编译、测试与运行 / Build, Test & Run

需要 Go 1.24 及以上版本：

```powershell
# 运行全部单元测试与集成测试
go test ./... -v

# 编译 Windows 可执行文件
go build -ldflags="-s -w" -o IPADownStudio.exe .

# 直接启动（自动以应用窗口打开界面）
.\IPADownStudio.exe

# 可选参数：指定监听端口、不自动弹窗、指定数据目录
.\IPADownStudio.exe --port 18990 --no-browser --data-dir .\data
```

## 合规与免责声明 / Compliance Notice

- 本仓库仅包含上层桌面端源码与前端界面，不分发任何 Apple 受版权保护的组件、`ipatool` 二进制文件或 `libunicorn.dll`。
- 本工具不绕过、修改或破解 App Store 的购买、DRM、FairPlay 签名、认证或下载授权规则；仅限下载你自己的 Apple 账号依法有权获取的应用版本。
- 局域网二维码传输仅传输原始 IPA 文件，不会在 iOS 上直接为应用签名或安装。

## 许可证 / License

本项目基于 [MIT License](LICENSE) 开源。
