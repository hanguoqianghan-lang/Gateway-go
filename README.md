# 工业物联网网关

基于Go语言开发的高性能工业物联网网关，支持Modbus TCP、IEC 60870-5-104等多种协议，通过配置文件驱动，支持分频采集和灵活的北向导出。

## 特性

- ✅ **配置驱动**：通过YAML配置文件和CSV点表文件实现灵活配置
- ✅ **多协议支持**：Modbus TCP、IEC 60870-5-104（纯Go实现）
- ✅ **多驱动实例**：支持同时连接多个Modbus从站或IEC104设备
- ✅ **分频采集**：支持不同测点设置不同的采集间隔
- ✅ **批量优化**：自动合并连续寄存器地址，减少请求次数
- ✅ **多导出器**：支持MQTT、Kafka、Console等多种北向导出方式
- ✅ **高性能**：基于sync.Pool实现零内存分配的测点对象池
- ✅ **断线检测**：自动检测连接状态，断线时发布质量戳
- ✅ **死区过滤**：支持绝对值和百分比两种死区类型
- ✅ **跨平台**：支持Windows、Linux、ARM64等多个平台
- ✅ **纯Go实现**：无CGO依赖，无痛交叉编译

## 快速开始

### 1. 编译程序

```bash
# 本机编译
go build -o gateway.exe ./cmd/gateway/

# 交叉编译ARM64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o gateway ./cmd/gateway/
```

### 2. 配置文件

编辑 `config/config.yaml` 文件，配置驱动和导出器：

```yaml
logging:
  level: info
  output: stdout

drivers:
  # IEC104驱动
  - name: iec104_device1
    type: iec104
    host: 192.168.1.100
    port: 2404
    common_address: 1
    point_file_path: points/iec104.csv

  # Modbus驱动
  - name: modbus_slave1
    type: modbus
    mode: tcp
    host: 192.168.1.200
    port: 502
    slave_id: 1
    point_file_path: points/modbus.csv

exporters:
  - name: console
    type: console
    enabled: true

  - name: mqtt_broker
    type: mqtt
    enabled: true
    broker: tcp://192.168.1.10:1883
    topic_prefix: cgn/gateway
```

### 3. 点表文件

编辑 `points/iec104.csv` 或 `points/modbus.csv` 文件，配置测点：

**IEC104点表**:
```csv
Name,IOA,CommonAddress,Type,Scale,Offset,DeadbandValue,DeadbandType,Description
voltage_a,100,1,M_ME_NC_1,1.0,0,0.1,absolute,A相电压
current_a,101,1,M_ME_NC_1,1.0,0,0.1,absolute,A相电流
switch1,1000,1,M_SP_NA_1,1.0,0,0,absolute,开关1状态
counter1,2000,1,M_IT_NA_1,1.0,0,0,absolute,电度累计量
```

**Modbus点表**:
```csv
Name,Address,Type,DataType,Scale,Offset,Interval,Description
temperature,100,holding,int16,0.1,0,1000,温度传感器
status,0,coil,bool,1.0,0,0,设备状态
```

### 4. 运行网关

```bash
# 使用默认配置
./gateway.exe

# 指定配置文件
./gateway.exe -config ./config/config.yaml
```

## 配置说明

详细的配置说明请参考 [CONFIG_GUIDE.md](./CONFIG_GUIDE.md)

### 主配置文件 (config.yaml)

#### 日志配置
```yaml
logging:
  level: info  # debug, info, warn, error
  output: stdout  # stdout, stderr, 或文件路径
```

#### IEC104驱动配置
```yaml
drivers:
  - id: iec104_device1      # 驱动唯一标识
    type: iec104            # 驱动类型
    name: iec104_device1    # 驱动名称（用于测点ID前缀）
    enabled: true           # 是否启用
    point_file: points/iec104.csv  # 点表文件路径
    iec104:
      host: 192.168.1.100           # 设备IP
      port: 2404                    # 端口（默认2404）
      common_address: 1             # 公共地址(CA)
      timeout: 10s                  # 连接超时
      test_interval: 20s            # 心跳测试间隔
      reconnect_interval: 5s        # 重连间隔
      gi_interval: 15m              # 总召唤间隔（0=不召唤）
      clock_sync_interval: 1h       # 时钟同步间隔（0=不同步）
      gi_staggered_delay: 5s        # 总召唤随机延迟（防风暴）
      enable_system_metrics: false  # 是否启用系统测点
```

#### Modbus驱动配置
```yaml
drivers:
  - id: modbus_slave1       # 驱动唯一标识
    type: modbus_tcp        # 驱动类型
    name: modbus_slave1     # 驱动名称（用于测点ID前缀）
    enabled: true           # 是否启用
    point_file: points/modbus.csv  # 点表文件路径
    modbus:
      host: 192.168.1.200           # 设备IP
      port: 502                     # 端口（默认502）
      unit_id: 1                    # 从站ID
      timeout: 3s                   # 请求超时
      poll_interval: 1s             # 轮询间隔
      max_retry_interval: 60s       # 最大重连间隔
```

### 点表文件说明

#### IEC104点表 (points/iec104.csv)
- **Name**: 测点名称（必填）
- **IOA**: 信息对象地址，0-16777215（必填）
- **CommonAddress**: 公共地址，0-255，0表示使用驱动默认值（可选）
- **Type**: 类型标识符（必填）
  - `M_SP_NA_1` - 单点遥信
  - `M_DP_NA_1` - 双点遥信
  - `M_ME_NA_1` - 归一化值
  - `M_ME_NB_1` - 标度化值
  - `M_ME_NC_1` - 短浮点数
  - `M_IT_NA_1` - 累计量
  - `M_ST_NA_1` - 步位置信息
  - `M_BO_NA_1` - 32位比特串
- **Scale**: 缩放系数，默认1.0（可选）
- **Offset**: 偏移量，默认0（可选）
- **DeadbandValue**: 死区阈值，默认0（可选）
- **DeadbandType**: 死区类型，absolute或percent，默认absolute（可选）
- **Description**: 测点描述（可选）

#### Modbus点表 (points/modbus.csv)
- **Name**: 测点名称（必填）
- **Address**: 寄存器地址，0-65535（必填）
- **Type**: 寄存器类型（必填）
  - `holding` - 保持寄存器（功能码03）
  - `input` - 输入寄存器（功能码04）
  - `coil` - 线圈（功能码01）
  - `discrete` - 离散输入（功能码02）
- **DataType**: 数据类型（必填）
  - `int16`, `uint16`, `int32`, `uint32`, `float32`, `float64`, `bool`
- **ByteOrder**: 字节序，big/little/ABCD/CDAB/BADC/DCBA，默认big（可选）
- **BitPos**: 位提取位置，0-15，-1表示不启用（可选）
- **Scale**: 缩放系数，默认1.0（可选）
- **Offset**: 偏移量，默认0（可选）
- **Interval**: 采集间隔（毫秒），0表示使用默认值（可选）
- **Description**: 测点描述（可选）

### 多驱动实例配置

#### 多个IEC104设备
```yaml
drivers:
  - id: iec104_device1
    type: iec104
    name: iec104_device1
    enabled: true
    point_file: points/iec104_device1.csv
    iec104:
      host: 192.168.1.100
      common_address: 1

  - id: iec104_device2
    type: iec104
    name: iec104_device2
    enabled: true
    point_file: points/iec104_device2.csv
    iec104:
      host: 192.168.1.101
      common_address: 1
```

#### 多个Modbus从站
```yaml
drivers:
  - id: modbus_slave1
    type: modbus_tcp
    name: modbus_slave1
    enabled: true
    point_file: points/modbus_slave1.csv
    modbus:
      host: 192.168.1.200
      unit_id: 1

  - id: modbus_slave2
    type: modbus_tcp
    name: modbus_slave2
    enabled: true
    point_file: points/modbus_slave2.csv
    modbus:
      host: 192.168.1.201
      unit_id: 1
```

## 项目结构

```
CGNgateway-go/
├── cmd/
│   └── gateway/           # 网关主程序
│       └── main.go
├── config/
│   ├── config.go          # 配置结构体定义
│   └── config.yaml        # 配置文件
├── internal/
│   ├── broker/            # 内部事件总线
│   ├── config/            # 配置加载器
│   ├── driver/            # 驱动接口和实现
│   │   ├── iec104/        # IEC104驱动(纯Go)
│   │   └── modbus/        # Modbus驱动
│   ├── exporter/          # 北向导出器
│   ├── factory/           # 驱动工厂
│   ├── model/             # 数据模型
│   └── point/             # 点表解析器
├── points/                # 点表文件目录
│   ├── iec104.csv         # IEC104点表
│   └── modbus.csv         # Modbus点表
├── Makefile               # 编译脚本
├── CONFIG_GUIDE.md        # 配置文件说明
└── README.md              # 本文件
```

## 性能优化

- **对象池**: 使用sync.Pool复用PointData对象，减少GC压力
- **批量处理**: 自动合并连续寄存器地址，减少网络请求
- **死区过滤**: 支持绝对值和百分比两种死区类型，过滤微小变化
- **高效索引**: 使用map实现O(1)点表查找（IEC104: key = (CA << 24) | IOA）
- **Worker Pool**: IEC104驱动使用Worker Pool并发处理ASDU报文，防止GI风暴阻塞
- **零拷贝**: 单订阅者场景下实现零拷贝数据转发
- **自动重连**: 指数退避重连机制，断线时自动发布质量戳

## IEC104 驱动特性

### 支持的 ASDU 类型

| 类型标识 | 名称 | 说明 |
|---------|------|------|
| M_SP_NA_1 | 单点遥信 | 布尔值，如开关状态 |
| M_DP_NA_1 | 双点遥信 | 四态值（不确定/分/合/异常） |
| M_ME_NA_1 | 归一化值 | -1.0 ~ 1.0，自动转换为百分比 |
| M_ME_NB_1 | 标度化值 | -32768 ~ 32767 |
| M_ME_NC_1 | 短浮点数 | IEEE 754 单精度浮点 |
| M_IT_NA_1 | 累计量 | 电度等累计值 |
| M_ST_NA_1 | 步位置信息 | 变压器档位等 |
| M_BO_NA_1 | 32位比特串 | 状态字 |

### 质量码映射

| IEC104 质量位 | 网关质量码 | 说明 |
|--------------|-----------|------|
| IV (Invalid) | QualityBad (0x80) | 数据无效 |
| NT (Not Topical) | QualityLastKnownValid (0xC8) | 非当前值 |
| SB (Substituted) | QualityUncertain (0x40) | 被取代 |
| BL (Blocked) | QualityUncertain (0x40) | 被封锁 |
| OV (Overflow) | QualityBad (0x80) | 溢出 |
| 正常 | QualityGood (0x00) | 数据正常 |
| 断线 | QualityNotConnected (0xC0) | 设备未连接 |

## 常见问题

### Q: 如何添加新的驱动？

A: 在 `internal/driver/` 下创建新的驱动包，实现 `driver.Driver` 接口，然后在驱动包的 `register.go` 中调用 `driver.RegisterDriver()` 注册。

### Q: 如何修改采集间隔？

A: 在点表CSV文件的 `Interval` 列中设置采集间隔（毫秒），0表示使用配置文件中的默认间隔。

### Q: 如何连接多个设备？

A: 在 `config.yaml` 中配置多个驱动实例，每个实例必须有唯一的 `name`。可以使用同一个CSV文件，通过CA/IOA或Address区分不同设备的测点。

### Q: 质量码含义？

A:
- `0`: 数据正常
- `64`: 数据不确定
- `128`: 数据无效
- `192`: 设备未连接

### Q: 如何启用调试日志？

A: 在 `config.yaml` 中设置 `logging.level` 为 `debug`。

## 技术栈

- **语言**: Go 1.24+
- **IEC104库**: github.com/wendy512/iec104 (纯Go实现)
- **Modbus库**: github.com/simonvetter/modbus
- **日志**: go.uber.org/zap
- **配置**: gopkg.in/yaml.v3

## 许可证

MIT License
