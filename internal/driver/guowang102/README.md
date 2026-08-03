# 国网102风光一体集中接入通讯系统驱动

## 概述

本驱动实现了《国网风光一体集中接入通讯系统102规约》，基于 IEC 60870-5-102 (DL/T 634.5104-2002) 扩展，专门用于风场/光伏电站功率预测系统的文件传输采集。

**核心特性：**
- 纯文件存储模式：子站上传的文件原样落盘，**不解析业务内容**
- 无需配置 CSV 点表，只需配置存储目录
- 支持多种文件类型：电量预测、短期/超短期预测、测风测光数据、机组状态
- 自动处理文件分帧、重组、去重、校验、超时清理

## 协议栈架构

```
┌─────────────────────────────────────────────────────┐
│                 GuoWang102 Driver                   │
├─────────────────────────────────────────────────────┤
│  Driver (driver.go) - 生命周期管理、配置、统计       │
├─────────────────────────────────────────────────────┤
│  Client (client.go) - TCP 连接、收发、重连           │
├─────────────────────────────────────────────────────┤
│  LinkLayer (handler.go) - 链路层状态机、FCB/ACD/DFC  │
├─────────────────────────────────────────────────────┤
│  FileTransferManager (file_transfer.go) - 文件传输状态机 │
├─────────────────────────────────────────────────────┤
│  Frame/ASDU (frame.go/asdu.go) - 编解码              │
└─────────────────────────────────────────────────────┘
```

## 配置说明

### YAML 配置项

```yaml
drivers:
  - id: "guowang102_1"
    type: "guowang102"
    enabled: true
    name: "guowang102_wind_farm"
    point_file: "./points/guowang102_dummy.csv"  # 仅占位
    guowang102:
      # 网络连接
      host: "192.168.1.100"          # 子站 IP
      port: 6960                     # 固定端口
      link_address: 65535            # 0xFFFF
      common_address: 65535          # 0xFFFF
      connect_timeout: "10s"
      read_timeout: "30s"
      write_timeout: "10s"
      keepalive_interval: "10s"

      # 协议流程
      link_status_interval: "60s"       # FC=9 链路状态检查
      background_scan_interval: "15m"   # FC=11 召唤2级数据
      periodic_read_interval: "5m"      # FC=10 召唤1级数据
      max_retry: 3
      retry_interval: "5s"
      frame_timeout: "5s"

      # 文件存储
      storage_dir: "./data/guowang102/files"
      max_file_size: 20480              # 512*40 字节
      file_timeout: "30s"

      log_level: "info"
```

### 关键参数说明

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `storage_dir` | `./data/guowang102/files` | 文件落盘目录，需可写 |
| `max_file_size` | 20480 | 单文件最大字节数 (512×40) |
| `file_timeout` | 30s | 单文件接收总超时 |
| `background_scan_interval` | 15m | FC=11 召唤2级数据间隔 |
| `periodic_read_interval` | 5m | FC=10 召唤1级数据间隔 |

## 文件命名规范

驱动按原文件名存储，文件名由子站上传帧中携带（32字节固定长度，去除填充后）。

典型文件类型对应 TypeID：
- 139 (0x8B): 电量预测文件
- 144 (0x90): 短期预测文件
- 145 (0x91): 超短期预测文件
- 146 (0x92): 测风/测光数据文件
- 147 (0x93): 机组/逆变器状态数据文件

## 通讯流程

```
1. 启动阶段
   主站(网关) → 发送复位链路(FC=0) → 子站
   子站      → 确认(0xE5/固定帧)    → 主站
   主站      → 发送启动数据传输(FC=4) → 子站
   子站      → 确认                → 主站
   链路进入 Operational 状态

2. 正常轮询 (主站主动)
   定时发送 FC=11 (召唤2级数据) ──→ 子站
   子站置位 ACD=1 回复确认
   主站检测 ACD=1 → 发送 FC=10 (召唤1级数据) ──→ 子站
   子站分帧发送文件数据 (COT=0x08/0x07) → 主站
   主站逐帧回 FC=3 确认
   文件结束 → 主站发 COT=0x0A (接收完成) → 子站回 COT=0x0B/0x0C

3. 子站主动上报 (ACD 机制)
   子站有数据 → 回复帧 ACD=1
   主站检测到 ACD=1 → 立即发 FC=10 召唤1级数据
   后续同正常轮询

4. 重连机制
   TCP 断开 → 指数退避重连 (5s~60s) → 重新链路初始化 → 恢复轮询
```

## 运行方式

```bash
# 使用示例配置启动
./gateway -config ./config/config_guowang102.yaml
```

## 统计监控

驱动暴露以下统计指标（通过 HTTP `/metrics` 或日志查看）：

```json
{
  "driver": "guowang102",
  "connected": true,
  "link_state": "Operational",
  "files_received": 10,
  "files_completed": 10,
  "files_failed": 0,
  "files_duplicated": 0,
  "bytes_received": 204800,
  "chunks_received": 400,
  "tx_frames": 1500,
  "rx_frames": 1480,
  "errors": 2,
  "reconnects": 1,
  "link_resets": 1,
  "acd_triggered": 5,
  "dfc_paused": 0
}
```

## 文件落盘结构

```
./data/guowang102/files/
├── WF_20240730_001.wpd   # 电量预测文件
├── ST_20240730_001.wpd   # 短期预测文件
├── US_20240730_001.wpd   # 超短期预测文件
├── MT_20240730_001.wpd   # 测风塔数据文件
└── UN_20240730_001.wpd   # 机组状态文件
```

文件内容为原始二进制/ASCII 格式，**不做任何解析转换**，供上层业务系统按需处理。

## 北向导出

文件落盘后，驱动发布 `EventTypeFileReceived` 事件到总线，北向导出器（MQTT/Kafka）可订阅该事件。

事件载荷示例：
```json
{
  "driver": "guowang102",
  "file_name": "WF_20240730_001.wpd",
  "file_size": 20480,
  "file_path": "./data/guowang102/files/WF_20240730_001.wpd"
}
```

## 测试

```bash
# 单元测试
go test ./internal/driver/guowang102/... -v

# 竞态检测
CGO_ENABLED=1 go test ./internal/driver/guowang102/... -race

# 全量测试
go test ./...
```

## 常见问题

### Q: 连接不上子站？
- 检查防火墙/安全组：子站 6960 端口需放行
- 确认子站 IP/端口配置正确
- 检查网络通达性：`telnet <host> 6960`

### Q: 文件接收不完整？
- 增加 `file_timeout`（大文件建议 60s+）
- 检查 `max_file_size` 是否覆盖实际文件大小
- 查看日志中的 `files_timeout` / `files_failed` 计数

### Q: 链路频繁复位？
- 检查 `frame_timeout` 是否过小（建议 ≥5s）
- 确认子站处理能力，适当增大 `retry_interval`
- 观察 `acd_triggered` 和 `dfc_paused` 计数

### Q: 重复文件处理？
- 驱动内置去重：已完成文件名+大小+时间记录
- 重复传输会返回 COT=0x0D/0x0E
- 完成态上下文保留 5 分钟防短时重复

## 开发参考

核心文件：
- `frame.go` / `frame_test.go` - FT1.2 帧编解码
- `asdu.go` / `asdu_test.go` - ASDU 编解码、文件传输字段
- `client.go` / `client_test.go` - TCP 客户端、重连
- `handler.go` - 链路层状态机、FCB/ACD/DFC
- `file_transfer.go` - 文件分帧重组、状态机、落盘
- `driver.go` - 驱动生命周期、轮询循环、配置映射
- `config.go` - 配置结构、默认值、校验
- `register.go` - 驱动工厂注册

## 版本历史

| 版本 | 日期 | 变更 |
|------|------|------|
| 1.0.0 | 2026-07-30 | 初始版本：完整协议栈、文件传输、配置集成 |

## 许可证

本项目遵循 Apache 2.0 许可证。