# 配置文件说明

## 配置文件结构

项目使用 YAML 格式的配置文件，主配置文件为 `config/config.yaml`。

## 主配置文件 (config.yaml)

### 完整示例

```yaml
# 网关基本信息
gateway:
  name: "Gateway"              # 网关名称
  version: "1.0.0"             # 版本号
  log_path: "./logs/gateway.log"  # 日志文件路径（可选，未配置则输出到控制台）
  log_level: "info"            # 日志级别：debug, info, warn, error
  log_max_size: 100            # 日志文件最大大小（MB）
  log_max_backups: 3           # 日志文件最大备份数
  log_max_age: 28              # 日志文件最大保留天数
  log_compress: true           # 是否压缩日志文件
  metrics_addr: ":8080"        # HTTP metrics 服务地址

# 南向驱动配置
drivers:
  # IEC104驱动示例
  - id: "iec104-01"            # 驱动实例唯一标识
    type: "iec104"             # 驱动类型：modbus_tcp, iec104, iec101, iec102, iec103, dlt645, gb26875, guowang102
    enabled: true              # 是否启用该驱动
    name: "substation-1"       # 驱动实例名称（用于日志和测点ID前缀）
    point_file: "./points/iec104.csv"  # 点表文件路径（CSV格式，相对于可执行文件）

    # IEC104 专用配置
    iec104:
      host: "192.168.1.100"    # IEC104设备IP地址
      port: 2404               # IEC104端口，默认2404
      common_address: 1        # 公共地址(CA)，默认1
      timeout: "10s"           # 连接超时，默认10s
      test_interval: "20s"     # 心跳测试间隔，默认20s
      reconnect_interval: "5s" # 重连间隔，默认5s
      gi_interval: "15m"       # 总召唤间隔，0=禁用，默认15m
      clock_sync_interval: "1h" # 时钟同步间隔，0=禁用，默认1h
      gi_staggered_delay: "5s" # 总召唤随机延迟上限，防止GI风暴，默认5s
      enable_system_metrics: true  # 是否启用系统测点，默认true
      asdu_buffer_size: 50000  # ASDU处理缓冲区大小，默认50000

  # 第二个IEC104驱动示例（连接另一个设备或同一设备的另一个CA）
  - id: "iec104-02"
    type: "iec104"
    enabled: true
    name: "substation-2"
    point_file: "./points/iec104.csv"
    iec104:
      host: "192.168.1.101"
      port: 2404
      common_address: 2

  # Modbus TCP驱动示例
  - id: "modbus-01"
    type: "modbus_tcp"
    enabled: true
    name: "plc-1"
    point_file: "./points/modbus.csv"

    modbus:
      host: "192.168.1.200"    # Modbus设备IP地址
      port: 502                # Modbus端口，默认502
      unit_id: 1               # 从站ID，默认1
      timeout: "3s"            # 请求超时，默认3s
      max_retry_interval: "60s" # 指数退避最大间隔，默认60s
      poll_interval: "1s"      # 默认采集轮询间隔，默认1s

  # Modbus RTU驱动示例（串口）
  - id: "modbus-rtu-01"
    type: "modbus_rtu"
    enabled: false
    name: "plc-rtu-1"
    point_file: "./points/modbus.csv"

    modbus_rtu:
      port: "COM3"             # 串口设备路径（如 COM3 或 /dev/ttyUSB0）
      baud_rate: 9600          # 波特率
      data_bits: 8             # 数据位
      stop_bits: 1             # 停止位
      parity: "even"           # 校验位：none, even, odd
      unit_id: 1               # 从站ID
      timeout: "3s"
      poll_interval: "1s"

  # DL/T 645驱动示例
  - id: "dlt645-01"
    type: "dlt645"
    enabled: true
    name: "meter-1"
    point_file: "./points/dlt645.csv"

    dlt645:
      serial_port: "COM4"      # 串口设备路径
      baud_rate: 9600          # 波特率
      data_bits: 8             # 数据位
      stop_bits: 1             # 停止位
      parity: "even"           # 校验位：none, even, odd
      protocol_version: "2007" # 协议版本：1997 或 2007
      use_leading_byte: false  # 是否使用前导字节（唤醒沉睡电表）
      leading_byte_count: 4    # 前导字节数量
      char_timeout: "50ms"     # 字符间超时
      frame_timeout: "200ms"   # 帧超时
      response_timeout: "1s"   # 响应超时
      max_retry: 3             # 最大重试次数
      retry_interval: "1s"     # 重试间隔
      poll_interval: "1s"      # 采集轮询间隔
      query_interval_per_point: "50ms" # 每测点查询间隔

  # IEC101驱动示例
  - id: "iec101-01"
    type: "iec101"
    enabled: false
    name: "iec101-test"
    point_file: "./points/iec101.csv"

    iec101:
      transport: "serial"      # 接入方式：serial | tcp
      serial_port: "COM3"      # 串口设备路径（transport=serial时必填）
      tcp_addr: "127.0.0.1:8881" # TCP地址（transport=tcp时必填）
      baud_rate: 9600
      data_bits: 8
      stop_bits: 1
      parity: "even"
      common_address: 1        # 公共地址
      link_address: 1          # 链路地址
      balanced_mode: false     # 传输模式：true=平衡，false=非平衡
      gi_interval: "15m"       # 总召唤间隔（非平衡模式）
      poll_interval: "1s"      # 轮询间隔
      timeout: "1s"            # 响应超时
      max_retry: 3
      retry_interval: "1s"

  # IEC102驱动示例
  - id: "iec102-01"
    type: "iec102"
    enabled: false
    name: "iec102-test"
    point_file: "./points/iec102.csv"

    iec102:
      serial_port: "COM3"
      baud_rate: 9600
      data_bits: 8
      stop_bits: 1
      parity: "even"
      common_address: 1
      link_address: 1
      balanced_mode: false
      background_scan_interval: "15m"
      periodic_read_interval: "5m"
      poll_interval: "1s"
      max_retry: 3
      retry_interval: "1s"

  # IEC103驱动示例
  - id: "iec103-01"
    type: "iec103"
    enabled: false
    name: "iec103-test"
    point_file: "./points/iec103.csv"

    iec103:
      serial_port: "COM3"
      baud_rate: 9600
      data_bits: 8
      stop_bits: 1
      parity: "even"
      common_address: 1
      link_address: 1
      balanced_mode: false
      gi_interval: "15m"
      poll_interval: "1s"
      max_retry: 3
      retry_interval: "1s"
      soe_queue_size: 10000
      soe_worker_count: 10

  # GB/T 26875.3 消防驱动示例
  - id: "gb26875-01"
    type: "gb26875"
    enabled: true
    name: "fire-1"
    point_file: "./points/gb26875.csv"

    gb26875:
      host: "0.0.0.0"          # 监听地址（0.0.0.0表示监听所有网卡）
      port: 5001               # TCP监听端口
      local_address: "000000000000" # 本机地址（6字节HEX，下行报文源地址）
      max_connections: 100     # 最大并发传输装置连接数
      read_timeout: "10s"      # 接收单帧超时
      write_timeout: "5s"      # 发送超时
      frame_timeout: "200ms"   # 相邻字节超时（用于切分帧）
      clock_sync_interval: "3600s" # 时钟同步周期（0=不主动同步）
      version: 1               # 主版本号（固定1）
      user_version: 1          # 用户版本号（自定义）
      enable_system_metrics: true # 是否启用系统测点
      adu_buffer_size: 5000    # 接收ADU缓冲区大小

  # 国网102风光一体驱动示例
  - id: "guowang102-01"
    type: "guowang102"
    enabled: false
    name: "guowang-1"
    point_file: "./points/guowang102.csv"

    guowang102:
      host: "192.168.1.100"    # 子站IP地址
      port: 6960               # 子站端口，固定6960
      link_address: 65535      # 链路地址，固定0xFFFF
      common_address: 65535    # 公共地址，固定0xFFFF
      connect_timeout: "10s"
      read_timeout: "30s"
      write_timeout: "10s"
      keepalive_interval: "10s"
      link_status_interval: "60s"
      background_scan_interval: "15m"
      periodic_read_interval: "5m"
      max_retry: 3
      retry_interval: "5s"
      frame_timeout: "5s"
      storage_dir: "./data/guowang102/files"
      max_file_size: 20480
      file_timeout: "30s"
      log_level: "info"

# 北向导出器配置
exporters:
  # MQTT导出器配置
  mqtt:
    enabled: true                      # 是否启用MQTT导出
    broker: "tcp://127.0.0.1:1883"     # MQTT broker地址
    client_id: "gateway"               # 客户端ID
    topic_prefix: "gateway/data"       # 发布主题前缀（完整topic = topic_prefix/<driver_name>）
    qos: 1                             # QoS级别：0, 1, 2
    username: ""                       # 用户名（可选）
    password: ""                       # 密码（可选）
    conn_timeout: "5s"                 # 连接超时

  # Kafka导出器配置
  # kafka:
  #   enabled: true
  #   brokers: ["127.0.0.1:9092"]      # Kafka broker列表
  #   topic: "gateway-data"            # 主题名称
  #   client_id: "gateway-producer"    # 客户端ID
  #   async: true                      # 是否异步写入（高吞吐）
  #   timeout: "5s"                    # 写入超时
  #   batch_size: 100                  # 批量大小
  #   batch_timeout: "10ms"            # 批量超时
  #   compression: "none"              # 压缩类型：none, gzip, snappy, lz4, zstd
  #   acks: 1                          # 确认级别：0=不确认, 1=leader确认, -1=all确认
  #   sasl:
  #     enabled: false
  #     mechanism: "PLAIN"             # PLAIN, SCRAM-SHA-256, SCRAM-SHA-512
  #     user: ""
  #     password: ""
  #   tls:
  #     enabled: false
  #     skip_verify: false
  #     cert_file: ""
  #     key_file: ""
  #     ca_file: ""

  # IEC104 Server导出器配置（北向，作为主站供SCADA连接）
  # iec104:
  #   enabled: false
  #   bind_addr: ":2405"               # 监听地址
  #   common_address: 1                # 公共地址(ASDU地址)
  #   max_connections: 5               # 最大客户端连接数
  #   point_map_file: "./points/iec104_server.csv" # 点表映射文件
  #   idle_timeout: "60s"              # 空闲连接超时
  #   interrogation_addr: 20           # 总召唤地址

# 批量发送配置（所有导出器共用）
batch:
  max_size: 500                # 批量发送最大条数
  max_latency: "200ms"         # 批量发送最大延迟

# 内部总线配置
bus:
  buffer_size: 8192            # 主通道缓冲区大小
  deadband_threshold: 0        # 死区阈值（0=禁用死区过滤）

# 离线缓存配置
storage:
  enabled: false               # 是否启用离线缓存
  type: "memory"               # 存储类型：memory, sqlite, leveldb
  path: "./data/gateway.db"    # 存储文件路径（sqlite/leveldb）
  max_memory_size: 100         # 内存缓存最大大小（MB）
  flush_interval: "30s"        # 刷盘间隔（仅memory类型有效）
  retry_interval: "10s"        # 重试间隔（网络恢复后）

# NTP时间同步配置
ntp:
  enabled: false               # 是否启用NTP时间同步
  server: "pool.ntp.org"       # NTP服务器地址
  port: 123                    # NTP服务器端口
  interval: "1h"               # 同步间隔
  timeout: "5s"                # 超时时间
```

## 多驱动实例配置说明

### IEC104 多驱动实例

当需要连接多个 IEC104 设备时，可以配置多个驱动实例：

```yaml
drivers:
  # 第一个IEC104设备
  - id: "iec104-01"
    type: "iec104"
    enabled: true
    name: "substation-1"
    point_file: "./points/iec104.csv"
    iec104:
      host: "192.168.1.100"
      common_address: 1

  # 第二个IEC104设备
  - id: "iec104-02"
    type: "iec104"
    enabled: true
    name: "substation-2"
    point_file: "./points/iec104.csv"  # 可以使用同一个CSV文件
    iec104:
      host: "192.168.1.101"
      common_address: 1

  # 第三个IEC104设备（或同一设备的另一个CA）
  - id: "iec104-03"
    type: "iec104"
    enabled: true
    name: "substation-3"
    point_file: "./points/iec104.csv"
    iec104:
      host: "192.168.1.100"  # 可以是同一个IP
      common_address: 2      # 不同的CA
```

**关键点：**
- 每个驱动实例必须有唯一的 `id` 和 `name`
- 可以连接到不同的 IP 地址（不同设备）
- 可以连接到同一个 IP 但不同的 CA（同一设备的不同公共地址）
- 可以使用同一个 CSV 文件，通过 CA 和 IOA 区分不同设备的测点
- 测点 ID 格式：`<driver_name>/iec104/<Name>`

### Modbus 多驱动实例

当需要连接多个 Modbus 从站时，可以配置多个驱动实例：

```yaml
drivers:
  # 第一个Modbus从站
  - id: "modbus-01"
    type: "modbus_tcp"
    enabled: true
    name: "plc-1"
    point_file: "./points/modbus.csv"
    modbus:
      host: "192.168.1.200"
      unit_id: 1

  # 第二个Modbus从站
  - id: "modbus-02"
    type: "modbus_tcp"
    enabled: true
    name: "plc-2"
    point_file: "./points/modbus.csv"
    modbus:
      host: "192.168.1.201"
      unit_id: 2

  # 第三个Modbus从站（同一设备不同从站ID）
  - id: "modbus-03"
    type: "modbus_tcp"
    enabled: true
    name: "plc-3"
    point_file: "./points/modbus.csv"
    modbus:
      host: "192.168.1.200"  # 可以是同一个IP
      unit_id: 3             # 不同的从站ID
```

**关键点：**
- 每个驱动实例必须有唯一的 `id` 和 `name`
- 可以连接到不同的 IP 地址（不同设备）
- 可以连接到同一个 IP 但不同的 `unit_id`（同一设备的不同从站）
- 可以使用同一个 CSV 文件，通过 Address 区分不同设备的测点
- 测点 ID 格式：`<driver_name>/modbus/<Name>`

### DL/T 645 多驱动实例

```yaml
drivers:
  - id: "dlt645-01"
    type: "dlt645"
    enabled: true
    name: "meter-1"
    point_file: "./points/dlt645.csv"
    dlt645:
      serial_port: "COM3"
      baud_rate: 9600

  - id: "dlt645-02"
    type: "dlt645"
    enabled: true
    name: "meter-2"
    point_file: "./points/dlt645.csv"
    dlt645:
      serial_port: "COM4"
      baud_rate: 2400
```

## CSV 点表文件说明

### IEC104 点表文件 (points/iec104.csv)

```csv
Name,IOA,CommonAddress,Type,Scale,Offset,DeadbandValue,DeadbandType,Description
voltage_a,100,1,M_ME_NC_1,1.0,0,0.1,absolute,A相电压
current_a,101,1,M_ME_NC_1,1.0,0,0.1,absolute,A相电流
switch1,1000,1,M_SP_NA_1,1.0,0,0,absolute,开关1状态
counter1,2000,1,M_IT_NA_1,1.0,0,0,absolute,电度累计量
```

**字段说明：**
- **Name**: 测点名称（必填），用于生成测点 ID
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

**关键点：**
- 多个驱动实例可以使用同一个 CSV 文件
- 通过 CA 和 IOA 的唯一组合来区分不同设备的测点
- 如果多个设备有相同的 CA+IOA，需要在 CSV 中为每个设备分别配置，或者使用不同的 CSV 文件

### Modbus 点表文件 (points/modbus.csv)

```csv
Name,Address,Type,DataType,ByteOrder,BitPos,Scale,Offset,Interval,UnitID,Description
temperature,100,holding,float32,BADC,-1,0.1,0,1000,1,温度传感器
pressure,102,holding,int16,big,-1,1.0,0,1000,1,压力传感器
status,0,coil,bool,big,0,1.0,0,0,1,设备状态
flow,104,input,uint32,ABCD,-1,0.01,0,2000,1,流量计
```

**字段说明：**
- **Name**: 测点名称（必填）
- **Address**: 寄存器地址，0-65535（必填）
- **Type**: 寄存器类型（必填）
  - `holding` - 保持寄存器（功能码03）
  - `input` - 输入寄存器（功能码04）
  - `coil` - 线圈（功能码01）
  - `discrete` - 离散输入（功能码02）
- **DataType**: 数据类型（必填）
  - `int16`, `uint16`, `int32`, `uint32`, `float32`, `float64`, `bool`
- **ByteOrder**: 字节序，默认big（可选）
  - `big` / `ABCD` - 大端序
  - `little` / `DCBA` - 小端序
  - `CDAB` - 字节序CDAB
  - `BADC` - 字节序BADC（国产设备常用）
- **BitPos**: 位提取位置，0-15，-1表示不启用（可选，仅bool类型或从寄存器提取位）
- **Scale**: 缩放系数，默认1.0（可选）
- **Offset**: 偏移量，默认0（可选）
- **Interval**: 采集间隔（毫秒），0表示使用默认值（可选）
- **UnitID**: 从站地址，0表示使用默认值（可选，用于将不同UnitID的测点分配到不同Slave）
- **Description**: 测点描述（可选）

**关键点：**
- 多个驱动实例可以使用同一个 CSV 文件
- 通过 Address 的唯一组合来区分不同设备的测点
- 如果多个设备有相同的 Address，需要在 CSV 中为每个设备分别配置，或者使用不同的 CSV 文件
- UnitID 字段可用于将同一个 CSV 中不同 UnitID 的测点自动分配到不同的 Slave 连接

### DL/T 645 点表文件 (points/dlt645.csv)

```csv
Name,Address,DataID,Scale,Offset,Unit,Precision,Interval,DeadbandValue,DeadbandType
voltage,010203040506,9011,0.1,0,V,1,1000,0.5,absolute
current,010203040506,9012,0.01,0,A,2,1000,0.1,absolute
energy,010203040506,9000,1.0,0,kWh,0,3600000,0,absolute
```

**字段说明：**
- **Name**: 测点名称（必填）
- **Address**: 表地址，12位BCD格式（必填），如 `010203040506` 表示地址 01 02 03 04 05 06
- **DataID**: 数据标识，4字节HEX（必填），如 `9011` 表示电压
- **Scale**: 缩放系数，默认1.0（可选）
- **Offset**: 偏移量，默认0（可选）
- **Unit**: 单位（可选）
- **Precision**: 小数位数，默认0（可选）
- **Interval**: 采集间隔（毫秒），默认使用驱动配置（可选）
- **DeadbandValue**: 死区阈值（可选）
- **DeadbandType**: 死区类型，absolute/percent（可选）

### IEC101 点表文件 (points/iec101.csv)

```csv
Name,CA,IOA,TypeID,Scale,Offset,Interval,DeadbandValue,DeadbandType
voltage_a,1,100,36,1.0,0,1000,0.1,absolute
switch1,1,1000,1,1.0,0,0,0,absolute
```

**字段说明：**
- **Name**: 测点名称（必填）
- **CA**: 公共地址，0-255（必填）
- **IOA**: 信息对象地址，0-65535（必填）
- **TypeID**: 类型标识，数值（必填），如 1=单点遥信，36=短浮点数
- **Scale**: 缩放系数（可选）
- **Offset**: 偏移量（可选）
- **Interval**: 采集间隔（毫秒）（可选）
- **DeadbandValue**: 死区阈值（可选）
- **DeadbandType**: 死区类型（可选）

### IEC102 点表文件 (points/iec102.csv)

格式同 IEC101。

### IEC103 点表文件 (points/iec103.csv)

格式同 IEC101，支持 SOE 事件类型。

### GB/T 26875.3 点表文件 (points/gb26875.csv)

```csv
Name,DeviceAddress,MessageType,SystemType,SystemAddress,ComponentType,ComponentAddr,AnalogType,AddrFormat,Scale,Offset,DeadbandValue,DeadbandType,Description
fire_alarm_1,,1,1,1,1,00000001,1,1,1.0,0,0,absolute,火警1
fault_1,,2,1,1,2,00000002,1,1,1.0,0,0,absolute,故障1
analog_1,,3,1,1,3,00000003,1,1,1.0,0,0.5,absolute,模拟量1
```

**字段说明：**
- **Name**: 测点名称（必填）
- **DeviceAddress**: 装置地址，6字节HEX（可选，为空则匹配所有装置）
- **MessageType**: 消息类型（必填）
  - 1: 火警信息
  - 2: 故障信息
  - 3: 模拟量信息
  - 4: 控制信息
  - 5: 状态信息
  - 89: 初始化
  - 90: 时钟同步
  - 91: 查岗
- **SystemType**: 系统类型（可选，默认0）
- **SystemAddress**: 系统地址（可选，默认0）
- **ComponentType**: 部件类型（可选）
- **ComponentAddr**: 部件地址，4字节HEX（可选）
- **AnalogType**: 模拟量类型（可选）
- **AddrFormat**: 地址格式（可选，默认1）
- **Scale**: 缩放系数（可选）
- **Offset**: 偏移量（可选）
- **DeadbandValue**: 死区阈值（可选）
- **DeadbandType**: 死区类型（可选）
- **Description**: 描述（可选）

### IEC104 Server 点表文件 (points/iec104_server.csv)

```csv
Name,IOA,TypeID,COT,Scale,Offset,CommonAddress
substation-1/iec104/voltage_a,100,36,1,1.0,0,1
substation-1/iec104/current_a,101,36,1,1.0,0,1
substation-1/iec104/switch1,1000,1,1,1.0,0,1
```

**字段说明：**
- **Name**: 内部测点 ID（必填），格式：`<driver_name>/iec104/<point_name>`
- **IOA**: IEC104 IOA 地址（必填）
- **TypeID**: 类型标识（必填）
- **COT**: 传送原因（必填）
- **Scale**: 缩放系数（可选）
- **Offset**: 偏移量（可选）
- **CommonAddress**: 公共地址（可选，默认使用导出器配置）

## 配置文件最佳实践

1. **使用 CSV 文件管理点表**：
   - 对于大量测点，推荐使用 CSV 文件而不是在 YAML 中直接配置
   - CSV 文件更易于编辑和维护（可用 Excel 编辑）
   - CSV 文件可以被版本控制

2. **多设备共享 CSV 文件**：
   - 如果设备的 CA/IOA 或 Address 不冲突，可以共享同一个 CSV 文件
   - 如果有冲突，建议为每个设备使用单独的 CSV 文件
   - CSV 文件命名建议：`points/<driver_name>.csv`

3. **驱动命名规范**：
   - 使用有意义的名称，如 `iec104_substation1`、`modbus_plc1`
   - 避免使用中文或特殊字符
   - `id` 和 `name` 在所有驱动实例中必须唯一

4. **系统测点**：
   - 启用系统测点可以监控驱动状态（`enable_system_metrics: true`）
   - 系统测点 ID 格式：`$<driver_name>/status`、`$<driver_name>/packet_loss_rate` 等
   - 建议在生产环境中启用

5. **日志级别**：
   - 开发调试时使用 `debug` 级别
   - 生产环境使用 `info` 或 `warn` 级别
   - 避免在生产环境使用 `debug` 级别，会影响性能

6. **死区过滤**：
   - 合理配置死区阈值可减少网络流量和存储压力
   - 绝对值死区：数值变化超过阈值才上报
   - 百分比死区：数值变化超过百分比才上报

7. **批量配置**：
   - 根据网络情况调整 `batch.max_size` 和 `batch.max_latency`
   - 高频数据建议增大批量大小、减小延迟
   - 低频数据可减小批量大小、增大延迟

## 环境变量覆盖

部分配置支持通过环境变量覆盖（需在代码中实现）：

```bash
# 示例（需代码支持）
export GATEWAY_LOG_LEVEL=debug
export GATEWAY_MQTT_BROKER=tcp://192.168.1.10:1883
```

## 配置验证

启动时会自动验证配置：
- 驱动 `id` 唯一性
- 必填字段非空
- 点表文件存在性
- 端口号范围
- 连接参数格式

验证失败会在日志中输出具体错误信息。