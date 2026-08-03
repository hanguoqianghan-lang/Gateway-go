# CLAUDE.md

本文件为 Claude Code 提供项目指导。

---

## 项目概述

工业物联网网关 - 基于 Go 1.24+ 开发，支持多种工业协议的高速数据采集和北向转发系统。

**核心特性：**
- 多协议南向驱动：Modbus TCP/RTU、IEC104、IEC101/102/103、DL/T 645、GB/T 26875.3、国网102
- 多北向导出器：MQTT、Kafka、IEC104 Server
- 配置驱动：YAML + CSV 点表
- 高性能：sync.Pool 对象池、批量优化、零拷贝
- 跨平台：纯 Go，无 CGO 依赖

---

## 常用命令

```bash
# 构建（输出到项目根目录，不是 bin 目录）
make build              # 本机编译到 gateway.exe
make build-windows     # Windows 版编译到 gateway.exe
make build-arm64       # 交叉编译 ARM64 (RK3568J / openEuler)
make build-arm         # 交叉编译 ARMv7
make build-all         # 编译所有平台

# 测试
make test                # 运行单元测试 (带竞态检测)
go test ./...            # 简化写法
go test -v ./internal/broker/       # 测试特定包
go test -v ./internal/driver/modbus/ # 测试 modbus merge

# 代码质量
make lint                # golangci-lint 静态检查
make tidy                # 整理 go.mod / go.sum

# 运行
./gateway.exe -config ./config/config.yaml
```

---

## 架构概览

```
南向驱动 (采集) ──→ Bus (内部事件总线) ──→ 北向导出器 (转发)
                    ↓
              sync.Pool (测点对象池)
```

### 核心接口

**Driver 接口** (`internal/driver/driver.go`):
- `Init(ctx) error` - 初始化驱动
- `Start(ctx, bus) error` - 启动采集
- `Stop(ctx) error` - 停止驱动
- `Name() string` - 驱动名称

**Exporter 接口** (`internal/exporter/exporter.go`):
- `Run(ctx, sub) error` - 运行导出器
- `Close()` - 关闭导出器
- `Name() string` - 导出器名称

---

## 驱动注册机制

新增驱动只需两步：
1. 在驱动包的 `register.go` 中调用 `driver.RegisterDriver()` 或 `exporter.RegisterExporter()`
2. 在 `internal/registry/registry.go` 添加 blank import

**已注册的南向驱动：**

| 类型 | 说明 | 配置前缀 |
|------|------|----------|
| `modbus_tcp` | Modbus TCP 主站 | modbus |
| `modbus_rtu` | Modbus RTU 主站 | modbus_rtu |
| `iec104` | IEC 60870-5-104 主站 | iec104 |
| `iec101` | IEC 60870-5-101 主站 | iec101 |
| `iec102` | IEC 60870-5-102 电能量 | iec102 |
| `iec103` | IEC 60870-5-103 继电保护 | iec103 |
| `dlt645` | DL/T 645-1997/2007 电能表 | dlt645 |
| `gb26875` | GB/T 26875.3 消防监控 | gb26875 |
| `guowang102` | 国网102 风光一体 | guowang102 |

**已注册的北向导出器：**

| 类型 | 说明 |
|------|------|
| `mqtt` | MQTT 消息发布 |
| `kafka` | Kafka 消息队列 |
| `iec104_server` | IEC104 Server (SCADA 主站) |

---

## PointData 生命周期 (关键)

```go
// 1. 获取 - 从 sync.Pool
p := model.GetPoint()

// 2. 填充 - 驱动层负责
p.ID = "device/iec104/point1"
p.Value = 123.45
p.Timestamp = time.Now().UnixNano()
p.Quality = model.QualityGood

// 3. 发布 - 所有权转移给 Bus
bus.Publish(p)

// 4. 归还 - Bus/Batcher 在消费后自动调用 PutPoint
// 注意：Publish 后不得再访问 p
```

**质量码：**
- `QualityGood` (0x00) - 数据正常
- `QualityUncertain` (0x40) - 数据不确定
- `QualityBad` (0x80) - 数据无效
- `QualityNotConnected` (0xC0) - 设备未连接

---

## 配置驱动模式

所有配置通过 YAML 文件 + CSV 点表文件：
- `config/config.yaml` - 主配置
- `points/*.csv` - 各驱动的点表文件

---

## 驱动实现要点

### Modbus TCP 驱动 (`internal/driver/modbus/`)

- 使用 `github.com/simonvetter/modbus` 库
- 寄存器批量优化：自动合并连续地址减少请求
- 每个 Slave 独立 goroutine 采集
- 支持寄存器类型：holding、input、coil、discrete
- 字节序支持：big、little、ABCD、CDAB、BADC、DCBA

### Modbus RTU 驱动 (`internal/driver/modbus/rtu.go`)

- 与 Modbus TCP 共享相同逻辑
- 使用 `rtu://` 协议前缀

### IEC104 驱动 (`internal/driver/iec104/`)

- 使用 `github.com/wendy512/iec104` 纯 Go 库
- 点表索引：`map[uint32]*pointMapping`，key = `(CA << 24) | IOA`
- ASDU Worker Pool：10 个 worker 并发处理
- 支持：总召唤(GI)、时钟同步、计数召唤、测试命令
- 可配置 ASDU 缓冲区大小 (默认 50000)

### IEC101 驱动 (`internal/driver/iec101/`)

- 支持 TCP 和串口两种传输模式
- 平衡/非平衡模式
- SOE (Sequence of Events) 支持

### IEC102 驱动 (`internal/driver/iec102/`)

- IEC 60870-5-102 电能量采集
- 串口传输

### IEC103 驱动 (`internal/driver/iec103/`)

- IEC 60870-5-103 继电保护
- SOE 大队列 (默认 10000)
- SOE Worker Pool (默认 10)

### DL/T 645 驱动 (`internal/driver/dlt645/`)

- 支持 DL/T 645-1997 和 DL/T 645-2007
- 前导字节 FE 支持（激活沉睡电表）
- 地址反转处理（帧中低字节在前）

### GB/T 26875.3 驱动 (`internal/driver/gb26875/`)

- 角色：监控中心（TCP Server）
- 监听端口，等待传输装置主动连接
- 支持主动下发：时钟同步(类型90)、初始化(89)、查岗(91)
- 点表索引：复合 key = type|sysaddr|comptype|component_addr
- 多连接管理，最大并发可配置

### 国网102 驱动 (`internal/driver/guowang102/`)

- 风光一体化采集协议
- 文件传输支持（遥测文件、SOE文件、配置文件）
- FC=9 链路状态、FC=10 一级数据、FC=11 二级数据
- 断点续传、本地文件存储

---

## 北向导出器实现要点

### MQTT 导出器 (`internal/exporter/mqtt.go`)

- 基于 `eclipse/paho.mqtt.golang`
- 支持：QoS 0/1/2、认证、保留消息、自动重连
- 主题格式：`{topic_prefix}/{driver_name}/{point_name}`
- 批量发送：按 slaveID 分组，JSON 序列化

### Kafka 导出器 (`internal/exporter/kafka.go`)

- 基于 `segmentio/kafka-go`
- 支持：异步写入、批量发送、压缩 (gzip/snappy/lz4/zstd)、SASL/TLS
- Hash 分区器：同一测点数据有序进入相同 Partition
- 使用 json-iterator 高性能 JSON 序列化

### IEC104 Server (`internal/exporter/iec104server/`)

- 将采集数据转发给 SCADA 系统
- 点表映射文件：`points/iec104_server.csv`
- 支持多客户端连接

---

## 关键文件路径

| 文件 | 说明 |
|------|------|
| `cmd/gateway/main.go` | 程序入口 |
| `config/config.go` | 配置结构体定义 |
| `config/config.yaml` | 配置文件示例 |
| `config/loader.go` | 配置加载器 |
| `internal/driver/driver.go` | 驱动接口定义 |
| `internal/driver/factory.go` | 驱动工厂 |
| `internal/driver/modbus/driver.go` | Modbus 驱动实现 |
| `internal/driver/iec104/driver.go` | IEC104 驱动实现 |
| `internal/driver/gb26875/driver.go` | GB26875 驱动实现 |
| `internal/driver/guowang102/driver.go` | 国网102 驱动实现 |
| `internal/exporter/exporter.go` | 导出器接口 |
| `internal/exporter/mqtt.go` | MQTT 导出器 |
| `internal/exporter/kafka.go` | Kafka 导出器 |
| `internal/exporter/batcher.go` | 批量发送器 |
| `internal/broker/bus.go` | 事件总线 |
| `internal/registry/registry.go` | 驱动/导出器注册 |
| `internal/point/parser.go` | CSV 点表解析 |
| `internal/model/point.go` | PointData 模型 |

---

## 添加新驱动的步骤

1. 创建 `internal/driver/<name>/driver.go`，实现 `driver.Driver` 接口
2. 创建 `internal/driver/<name>/register.go`，在 `init()` 中注册：
   ```go
   func init() {
       driver.RegisterDriver("<type_name>", NewDriverFromConfig)
   }
   ```
3. 在 `config/config.go` 添加配置结构体 (如 `XXXDriverConfig`)
4. 在 `internal/config/loader.go` 添加默认值填充逻辑
5. 在 `internal/registry/registry.go` 添加 blank import：
   ```go
   _ "github.com/gateway/gateway/internal/driver/<name>"
   ```
6. 添加 CSV 点表解析逻辑（参考现有驱动的 register.go）
7. 添加单元测试（如有）

---

## CSV 点表格式

### Modbus 点表 (`points/*.csv`)

```csv
Name,Address,Type,DataType,ByteOrder,BitPos,Scale,Offset,Interval,UnitID,Description
```

| 字段 | 说明 | 示例 |
|------|------|------|
| Name | 测点名称 | `temperature` |
| Address | 寄存器地址 (0-65535) | `100` |
| Type | holding/input/coil/discrete | `holding` |
| DataType | int16/uint16/int32/uint32/float32/float64/bool | `float32` |
| ByteOrder | big/little/ABCD/CDAB/BADC/DCBA | `BADC` |
| BitPos | 位提取位置 0-15，-1=不启用 | `-1` |
| Scale | 缩放系数 | `0.1` |
| Offset | 偏移量 | `0` |
| Interval | 采集间隔(ms)，0=默认 | `1000` |
| UnitID | 从站地址(0=默认) | `1` |
| Description | 描述 | `温度传感器` |

### IEC104 点表 (`points/iec104.csv`)

```csv
Name,IOA,CommonAddress,Type,Scale,Offset,DeadbandValue,DeadbandType,Description
```

| 字段 | 说明 | 示例 |
|------|------|------|
| Name | 测点名称 | `voltage_a` |
| IOA | 信息对象地址 (0-16777215) | `100` |
| CommonAddress | 公共地址 (0-255)，0=使用驱动默认 | `1` |
| Type | M_SP_NA_1/M_DP_NA_1/M_ME_NC_1/M_IT_NA_1... | `M_ME_NC_1` |
| Scale | 缩放系数 | `1.0` |
| Offset | 偏移量 | `0` |
| DeadbandValue | 死区阈值 | `0.1` |
| DeadbandType | absolute/percent | `absolute` |
| Description | 描述 | `A相电压` |

**IEC104 类型标识符：**
- `M_SP_NA_1` - 单点遥信
- `M_DP_NA_1` - 双点遥信
- `M_ME_NA_1` - 归一化值 (-1.0~1.0)
- `M_ME_NB_1` - 标度化值 (-32768~32767)
- `M_ME_NC_1` - 短浮点数 (IEEE 754)
- `M_IT_NA_1` - 累计量
- `M_ST_NA_1` - 步位置信息
- `M_BO_NA_1` - 32位比特串

### IEC101 点表 (`points/iec101.csv`)

```csv
Name,CA,IOA,TypeID,Scale,Offset,Interval,DeadbandValue,DeadbandType
```

### IEC102 点表 (`points/iec102.csv`)

```csv
Name,CA,IOA,TypeID,Scale,Offset,Interval,DeadbandValue,DeadbandType
```

### IEC103 点表 (`points/iec103.csv`)

```csv
Name,CA,IOA,TypeID,Scale,Offset,Interval,DeadbandValue,DeadbandType
```

### DL/T 645 点表 (`points/dlt645.csv`)

```csv
Name,Address,DataID,Scale,Offset,Unit,Precision,Interval,DeadbandValue,DeadbandType
```

| 字段 | 说明 | 示例 |
|------|------|------|
| Name | 测点名称 | `voltage` |
| Address | 表地址 (12位BCD，如 010203040506) | `010203040506` |
| DataID | 数据标识 (4字节HEX) | `9011` |
| Scale | 缩放系数 | `0.1` |
| Offset | 偏移量 | `0` |
| Unit | 单位 | `V` |
| Precision | 小数位数 | `1` |
| Interval | 采集间隔(ms) | `1000` |
| DeadbandValue | 死区阈值 | `0.5` |
| DeadbandType | absolute/percent | `absolute` |

### GB/T 26875.3 点表 (`points/gb26875.csv`)

```csv
Name,DeviceAddress,MessageType,SystemType,SystemAddress,ComponentType,ComponentAddr,AnalogType,AddrFormat,Scale,Offset,DeadbandValue,DeadbandType,Description
```

| 字段 | 说明 | 示例 |
|------|------|------|
| Name | 测点名称 | `fire_alarm_1` |
| DeviceAddress | 装置地址 (6字节HEX，可选) | `800D00000000` |
| MessageType | 消息类型 (必填) | `1` |
| SystemType | 系统类型 | `1` |
| SystemAddress | 系统地址 | `1` |
| ComponentType | 部件类型 | `1` |
| ComponentAddr | 部件地址 (4字节HEX) | `00000001` |
| AnalogType | 模拟量类型 | `1` |
| AddrFormat | 地址格式 (默认1) | `1` |
| Scale | 缩放系数 | `1.0` |
| Offset | 偏移量 | `0` |
| DeadbandValue | 死区阈值 | `0` |
| DeadbandType | absolute/percent | `absolute` |
| Description | 描述 | `火警1` |

### IEC104 Server 点表 (`points/iec104_server.csv`)

```csv
Name,IOA,TypeID,COT,Scale,Offset,CommonAddress
```

| 字段 | 说明 |
|------|------|
| Name | 内部测点 ID (格式: driver/iec104/point_name) |
| IOA | IEC104 IOA 地址 |
| TypeID | 类型标识 |
| COT | 传送原因 |
| Scale | 缩放系数 |
| Offset | 偏移量 |
| CommonAddress | 公共地址 |

---

## 常见问题排查

### Q: Modbus 数据解析错误？
检查字节序配置：
- 大多数设备用 `big` (ABCD)
- 国产设备常用 `BADC` (BA DC)
- 尝试不同字节序直到数据正确

### Q: 串口打开失败 "Access is denied"？
串口被其他程序占用，关闭其他使用该串口的程序。

### Q: 启动时配置验证失败？
检查 `config.yaml` 中：
- 驱动 ID 不能重复
- 点表文件路径必须存在
- 必填字段不能为空

### Q: 数据采集正常但 MQTT 无数据？
检查：
- MQTT Broker 是否可达
- `exporters.mqtt.enabled` 是否为 true
- 主题前缀是否正确

### Q: IEC104 总召唤不执行？
检查：
- `gi_interval` > 0 (如 `15m`)
- 点表中有对应类型测点
- 日志查看 "发送总召唤" 记录

### Q: DL/T 645 读取无响应？
检查：
- 串口参数 (波特率、校验位) 与电表一致
- 地址格式正确 (12位BCD)
- 是否需要前导字节唤醒 (`use_leading_byte: true`)

---

## 技术栈

| 组件 | 库 |
|------|-----|
| IEC104 协议 | github.com/wendy512/iec104 (纯 Go) |
| Modbus 协议 | github.com/simonvetter/modbus |
| 串口通信 | github.com/goburrow/serial |
| 日志 | go.uber.org/zap |
| MQTT | github.com/eclipse/paho.mqtt.golang |
| Kafka | github.com/segmentio/kafka-go |
| YAML 解析 | gopkg.in/yaml.v3 |
| JSON | github.com/json-iterator/go |

---

## 测试要点

```bash
# 运行所有测试（含竞态检测）
make test

# 测试 Modbus 寄存器合并逻辑
go test -v ./internal/driver/modbus/ -run TestMerge

# 测试 IEC104 点表索引
go test -v ./internal/driver/iec104/ -run TestPointMap

# 测试 Broker 总线
go test -v ./internal/broker/

# 测试配置加载
go test -v ./internal/config/
```