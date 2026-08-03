# 工业物联网网关

基于 Go 1.24+ 开发的高性能工业物联网网关，支持多种工业协议的高速数据采集和北向转发系统。

## 特性

- ✅ **多协议南向驱动**：Modbus TCP/RTU、IEC104、IEC101、IEC102、IEC103、DL/T 645、GB/T 26875.3、国网102
- ✅ **多北向导出器**：MQTT、Kafka、IEC104 Server
- ✅ **配置驱动**：YAML 配置 + CSV 点表文件
- ✅ **高性能**：sync.Pool 对象池、寄存器批量合并、ASDU Worker Pool、零拷贝
- ✅ **跨平台**：纯 Go 实现，无 CGO 依赖，无痛交叉编译
- ✅ **多驱动实例**：支持同时连接多个设备/从站
- ✅ **分频采集**：支持不同测点设置不同的采集间隔
- ✅ **死区过滤**：支持绝对值和百分比两种死区类型
- ✅ **自动重连**：指数退避重连机制，断线时自动发布质量戳
- ✅ **系统测点**：内置连接状态、丢包率等监控指标

## 架构概览

```
南向驱动 (采集) ──→ Bus (内部事件总线) ──→ 北向导出器 (转发)
                    ↓
              sync.Pool (测点对象池)
```

### 核心组件

| 组件 | 说明 |
|------|------|
| **Driver 接口** | `Init/Start/Stop/Name` - 统一驱动生命周期 |
| **Exporter 接口** | `Run/Close/Name` - 统一导出器生命周期 |
| **Bus** | 内部事件总线，支持死区过滤、订阅广播 |
| **Model** | `PointData` - 统一数据模型，含 ID、Value、Timestamp、Quality |
| **Registry** | 驱动/导出器自动注册机制 |

## 快速开始

### 1. 编译程序

```bash
# 本机编译（Windows）
make build

# 交叉编译 ARM64 (RK3568J / openEuler)
make build-arm64

# 交叉编译 ARMv7
make build-arm

# 交叉编译 Linux AMD64
make build-linux

# 构建所有平台
make build-all
```

或直接使用 Go 命令：

```bash
# 本机编译
go build -o gateway.exe ./cmd/gateway/

# 交叉编译 ARM64 (CGO_ENABLED=0 关键：纯Go无CGO依赖)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o gateway ./cmd/gateway/
```

### 2. 配置文件

编辑 `config/config.yaml`，配置驱动和导出器：

```yaml
gateway:
  name: "Gateway"
  version: "1.0.0"
  log_level: "info"
  metrics_addr: ":8080"

drivers:
  # IEC104驱动示例
  - id: "iec104-01"
    type: "iec104"
    enabled: true
    name: "substation-1"
    point_file: "./points/iec104.csv"
    iec104:
      host: "192.168.1.100"
      port: 2404
      common_address: 1
      gi_interval: "15m"
      clock_sync_interval: "1h"

  # Modbus TCP驱动示例
  - id: "modbus-01"
    type: "modbus_tcp"
    enabled: true
    name: "plc-1"
    point_file: "./points/modbus.csv"
    modbus:
      host: "192.168.1.200"
      port: 502
      unit_id: 1
      poll_interval: "1s"

  # DL/T 645 驱动示例
  - id: "dlt645-01"
    type: "dlt645"
    enabled: true
    name: "meter-1"
    point_file: "./points/dlt645.csv"
    dlt645:
      serial_port: "COM3"
      baud_rate: 9600
      protocol_version: "2007"
      poll_interval: "1s"

  # GB/T 26875.3 消防驱动示例
  - id: "gb26875-01"
    type: "gb26875"
    enabled: true
    name: "fire-1"
    point_file: "./points/gb26875.csv"
    gb26875:
      host: "0.0.0.0"
      port: 5001
      local_address: "000000000000"

  # 国网102风光一体驱动示例
  - id: "guowang102-01"
    type: "guowang102"
    enabled: true
    name: "guowang-1"
    point_file: "./points/guowang102.csv"
    guowang102:
      host: "192.168.1.100"
      port: 6960

exporters:
  mqtt:
    enabled: true
    broker: "tcp://127.0.0.1:1883"
    client_id: "gateway"
    topic_prefix: "gateway/data"
    qos: 1

  # kafka:
  #   enabled: true
  #   brokers: ["127.0.0.1:9092"]
  #   topic: "gateway-data"
  #   async: true

  # iec104:
  #   enabled: true
  #   bind_addr: ":2405"
  #   point_map_file: "./points/iec104_server.csv"

batch:
  max_size: 500
  max_latency: "200ms"
```

### 3. 点表文件

#### IEC104 点表 (`points/iec104.csv`)
```csv
Name,IOA,CommonAddress,Type,Scale,Offset,DeadbandValue,DeadbandType,Description
voltage_a,100,1,M_ME_NC_1,1.0,0,0.1,absolute,A相电压
current_a,101,1,M_ME_NC_1,1.0,0,0.1,absolute,A相电流
switch1,1000,1,M_SP_NA_1,1.0,0,0,absolute,开关1状态
counter1,2000,1,M_IT_NA_1,1.0,0,0,absolute,电度累计量
```

#### Modbus 点表 (`points/modbus.csv`)
```csv
Name,Address,Type,DataType,ByteOrder,BitPos,Scale,Offset,Interval,UnitID,Description
temperature,100,holding,float32,BADC,-1,0.1,0,1000,1,温度传感器
pressure,102,holding,int16,big,-1,1.0,0,1000,1,压力传感器
status,0,coil,bool,big,0,1.0,0,0,1,设备状态
```

#### DL/T 645 点表 (`points/dlt645.csv`)
```csv
Name,Address,DataID,Scale,Offset,Unit,Precision,Interval,DeadbandValue,DeadbandType
voltage,010203040506,9011,0.1,0,V,1,1000,0.5,absolute
current,010203040506,9012,0.01,0,A,2,1000,0.1,absolute
energy,010203040506,9000,1.0,0,kWh,0,3600000,0,absolute
```

### 4. 运行网关

```bash
# 使用默认配置
./gateway.exe

# 指定配置文件
./gateway.exe -config ./config/config.yaml
```

## 配置说明

详细配置说明请参考 [CONFIG_GUIDE.md](./CONFIG_GUIDE.md)

### 主要配置项

| 配置节 | 说明 |
|--------|------|
| `gateway` | 网关基本信息、日志、Metrics 端口 |
| `drivers` | 南向驱动列表，每个驱动含 ID、类型、连接参数、点表路径 |
| `exporters` | 北向导出器：MQTT、Kafka、IEC104 Server |
| `bus` | 内部总线缓冲区、死区阈值 |
| `batch` | 批量发送配置（最大条数、最大延迟） |
| `storage` | 离线缓存配置 |
| `ntp` | NTP 时间同步配置 |

## 项目结构

```
gateway-go/
├── cmd/
│   └── gateway/           # 网关主程序入口
│       └── main.go
├── config/
│   ├── config.go          # 配置结构体定义
│   ├── config.yaml        # 配置文件示例
│   └── loader.go          # 配置加载器
├── internal/
│   ├── broker/            # 内部事件总线
│   │   └── bus.go
│   ├── config/            # 配置加载器
│   │   └── loader.go
│   ├── driver/            # 驱动接口和实现
│   │   ├── driver.go      # 驱动接口定义
│   │   ├── factory.go     # 驱动工厂
│   │   ├── modbus/        # Modbus TCP/RTU 驱动
│   │   ├── iec104/        # IEC104 驱动
│   │   ├── iec101/        # IEC101 驱动
│   │   ├── iec102/        # IEC102 驱动
│   │   ├── iec103/        # IEC103 驱动
│   │   ├── dlt645/        # DL/T 645 驱动
│   │   ├── gb26875/       # GB/T 26875.3 消防驱动
│   │   └── guowang102/    # 国网102风光一体驱动
│   ├── exporter/          # 北向导出器
│   │   ├── exporter.go    # 导出器接口
│   │   ├── mqtt.go        # MQTT 导出器
│   │   ├── kafka.go       # Kafka 导出器
│   │   └── batcher.go     # 批量发送器
│   ├── model/             # 数据模型
│   │   └── point.go       # PointData 定义
│   ├── point/             # 点表解析器
│   │   └── parser.go
│   └── version/           # 版本信息
├── points/                # 点表文件目录
│   ├── iec104.csv
│   ├── modbus.csv
│   ├── dlt645.csv
│   ├── iec101.csv
│   ├── iec102.csv
│   ├── iec103.csv
│   ├── gb26875.csv
│   └── guowang102.csv
├── Makefile               # 编译脚本
├── CONFIG_GUIDE.md        # 配置文件详细说明
├── CLAUDE.md              # Claude Code 项目指导
└── README.md              # 本文件
```

## 支持的协议详情

### 南向驱动

| 驱动类型 | 协议标准 | 传输方式 | 关键特性 |
|---------|---------|---------|---------|
| `modbus_tcp` | Modbus TCP | TCP | 寄存器批量合并、多字节序支持、多Slave并发 |
| `modbus_rtu` | Modbus RTU | 串口 | 与 TCP 共享逻辑、rtu:// 前缀 |
| `iec104` | IEC 60870-5-104 | TCP | 纯Go库、O(1)点表索引、ASDU Worker Pool、GI/时钟同步 |
| `iec101` | IEC 60870-5-101 | 串口/TCP | 平衡/非平衡模式、SOE支持 |
| `iec102` | IEC 60870-5-102 | 串口 | 电能量采集 |
| `iec103` | IEC 60870-5-103 | 串口 | 继电保护、SOE大队列 |
| `dlt645` | DL/T 645-1997/2007 | 串口 | 前导字节唤醒、多协议版本 |
| `gb26875` | GB/T 26875.3-2011 | TCP Server | 消防监控中心、多连接管理、主动下发命令 |
| `guowang102` | 国网102规约 | TCP | 风光一体、文件传输、FC9/10/11服务 |

### 北向导出器

| 导出器类型 | 协议 | 关键特性 |
|-----------|------|---------|
| `mqtt` | MQTT 3.1/5.0 | QoS 0/1/2、自动重连、分组发布、JSON 批量 |
| `kafka` | Apache Kafka | Async/Sync、SASL/TLS、压缩、Hash分区、批量 |
| `iec104` | IEC 60870-5-104 Server | 点表映射、多客户端、SCADA兼容 |

## 质量码定义

| 质量码 | 值 | 说明 |
|--------|-----|------|
| `QualityGood` | 0x00 | 数据正常 |
| `QualityUncertain` | 0x40 | 数据不确定 |
| `QualityBad` | 0x80 | 数据无效 |
| `QualityNotConnected` | 0xC0 | 设备未连接 |

## 性能优化亮点

- **对象池**: `sync.Pool` 复用 `PointData`，减少 GC 压力
- **批量合并**: Modbus 自动合并连续寄存器地址，减少网络请求
- **高效索引**: IEC104 使用 `map[uint32]` 实现 O(1) 点表查找 (key = CA<<24 \| IOA)
- **Worker Pool**: IEC104 ASDU 处理使用 10 个 Worker 并发，防止 GI 风暴阻塞
- **零拷贝**: 单订阅者场景下实现零拷贝数据转发
- **自动重连**: 指数退避机制，断线时发布 QualityNotConnected 质量戳

## 常用命令

```bash
# 构建
make build              # 本机编译到 gateway.exe
make build-windows     # Windows 版编译
make build-arm64       # ARM64 (RK3568J / openEuler)
make build-arm         # ARMv7
make build-all         # 所有平台

# 测试
make test               # 单元测试 (带竞态检测)
go test ./...           # 简化写法
go test -v ./internal/broker/       # 特定包测试
go test -v ./internal/driver/modbus/ # Modbus merge 测试

# 代码质量
make lint               # golangci-lint 静态检查
make tidy               # 整理 go.mod / go.sum

# 运行
./gateway.exe -config ./config/config.yaml
```

## 常见问题排查

### Q: Modbus 数据解析错误？
检查字节序配置：
- 大多数设备用 `big` (ABCD)
- 国产设备常用 `BADC` (BA DC)
- 尝试不同字节序：`big`/`little`/`ABCD`/`CDAB`/`BADC`/`DCBA`

### Q: 串口打开失败 "Access is denied"？
串口被其他程序占用，关闭其他使用该串口的程序（如串口调试助手）。

### Q: 启动时配置验证失败？
检查 `config.yaml`：
- 驱动 `id` 不能重复
- 点表文件路径必须存在（相对于可执行文件或绝对路径）
- 必填字段不能为空（如 host、port、point_file）

### Q: 数据采集正常但 MQTT 无数据？
检查：
- MQTT Broker 是否可达（telnet 测试端口）
- `exporters.mqtt.enabled: true`
- `topic_prefix` 格式正确
- 日志中是否有 "MQTT 连接成功"

### Q: IEC104 总召唤不触发？
检查：
- `gi_interval` > 0（如 `15m`）
- 点表中有对应类型的测点
- 日志查看 "发送总召唤" 记录

## 技术栈

| 组件 | 库 | 版本 |
|------|-----|------|
| 语言 | Go | 1.24+ |
| IEC104 协议 | github.com/wendy512/iec104 | 纯 Go |
| Modbus 协议 | github.com/simonvetter/modbus | - |
| 串口通信 | github.com/goburrow/serial | - |
| 日志 | go.uber.org/zap | - |
| MQTT | github.com/eclipse/paho.mqtt.golang | - |
| Kafka | github.com/segmentio/kafka-go | - |
| YAML 解析 | gopkg.in/yaml.v3 | - |
| JSON | github.com/json-iterator/go | 高性能 |

## 开发指南

### 添加新驱动

1. 在 `internal/driver/<name>/` 创建驱动包
2. 实现 `driver.Driver` 接口 (`Init/Start/Stop/Name`)
3. 创建 `register.go`，在 `init()` 中注册：
   ```go
   func init() {
       driver.RegisterDriver("<type_name>", NewDriverFromConfig)
   }
   ```
4. 在 `config/config.go` 添加对应的 `DriverConfig` 结构体
5. 在 `internal/registry/registry.go` 添加 blank import：
   ```go
   _ "github.com/gateway/gateway/internal/driver/<name>"
   ```
6. 添加 CSV 解析逻辑（参考现有驱动）
7. 添加单元测试

### 添加新导出器

1. 在 `internal/exporter/` 实现 `exporter.Exporter` 接口
2. 在 `main.go` 的 `initExporters()` 中实例化并启动

## 许可证

MIT License