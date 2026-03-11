# 配置文件说明

## 配置文件结构

项目使用YAML格式的配置文件,主配置文件为 `config/config.yaml`。

## 主配置文件 (config.yaml)

### 完整示例

```yaml
# 日志配置
logging:
  level: info  # 日志级别: debug, info, warn, error
  output: stdout  # 输出方式: stdout, stderr, 或文件路径

# 驱动配置
drivers:
  # IEC104驱动示例
  - name: iec104_device1  # 驱动唯一名称
    type: iec104  # 驱动类型: iec104, modbus
    host: 192.168.1.100  # IEC104设备IP地址
    port: 2404  # IEC104端口,默认2404
    common_address: 1  # 公共地址(CA),默认1
    reconnect_interval: 5s  # 重连间隔,默认5s
    timeout: 10s  # 连接超时,默认10s
    gi_interval: 15m  # 总召唤间隔,0=禁用,默认15m
    clock_sync_interval: 1h  # 时钟同步间隔,0=禁用,默认1h
    gi_staggered_delay: 30s  # 总召唤随机延迟,防止GI风暴,默认30s
    enable_system_metrics: true  # 是否启用系统测点,默认true
    # 点表配置方式1: 直接在YAML中配置
    points:
      - name: voltage_a
        ca: 1
        ioa: 100
        type_id: M_ME_NC_1
        scale: 1.0
        offset: 0.0
        deadband_value: 0.1
        deadband_type: absolute
      - name: switch1
        ca: 1
        ioa: 1000
        type_id: M_SP_NA_1
        scale: 1.0
        offset: 0.0
    # 点表配置方式2: 从CSV文件加载(推荐)
    point_file_path: points/iec104.csv  # CSV文件路径

  # 第二个IEC104驱动示例(连接到另一个设备或同一设备的另一个CA)
  - name: iec104_device2
    type: iec104
    host: 192.168.1.101
    port: 2404
    common_address: 2
    reconnect_interval: 5s
    timeout: 10s
    gi_interval: 15m
    clock_sync_interval: 1h
    point_file_path: points/iec104.csv  # 可以使用同一个CSV文件

  # Modbus驱动示例
  - name: modbus_slave1  # 驱动唯一名称
    type: modbus  # 驱动类型: iec104, modbus
    mode: tcp  # 连接模式: tcp, rtu
    host: 192.168.1.200  # Modbus设备IP地址
    port: 502  # Modbus端口,默认502
    slave_id: 1  # 从站ID,默认1
    poll_interval: 1s  # 轮询间隔,默认1s
    timeout: 5s  # 超时时间,默认5s
    reconnect_interval: 5s  # 重连间隔,默认5s
    enable_system_metrics: true  # 是否启用系统测点,默认true
    # 点表配置方式1: 直接在YAML中配置
    points:
      - name: temperature
        address: 100
        type: holding
        data_type: int16
        scale: 0.1
        offset: 0.0
        interval: 1000
      - name: status
        address: 0
        type: coil
        data_type: bool
        scale: 1.0
        offset: 0.0
        interval: 0
    # 点表配置方式2: 从CSV文件加载(推荐)
    point_file_path: points/modbus.csv  # CSV文件路径

  # 第二个Modbus驱动示例(连接到另一个从站)
  - name: modbus_slave2
    type: modbus
    mode: tcp
    host: 192.168.1.201
    port: 502
    slave_id: 2
    poll_interval: 1s
    timeout: 5s
    reconnect_interval: 5s
    point_file_path: points/modbus.csv  # 可以使用同一个CSV文件

# 导出器配置
exporters:
  - name: console  # 导出器名称
    type: console  # 导出器类型: console, mqtt
    enabled: true  # 是否启用

  - name: mqtt_broker
    type: mqtt
    enabled: true  # 是否启用
    broker: tcp://192.168.1.10:1883  # MQTT代理地址
    client_id: gateway  # 客户端ID
    username: user  # 用户名(可选)
    password: pass  # 密码(可选)
    topic_prefix: gateway  # 主题前缀
    qos: 1  # QoS级别: 0, 1, 2
    retain: false  # 是否保留消息
```

## 多驱动实例配置说明

### IEC104多驱动实例

当需要连接多个IEC104设备时,可以配置多个驱动实例:

```yaml
drivers:
  # 第一个IEC104设备
  - name: iec104_device1
    type: iec104
    host: 192.168.1.100
    port: 2404
    common_address: 1
    point_file_path: points/iec104.csv

  # 第二个IEC104设备
  - name: iec104_device2
    type: iec104
    host: 192.168.1.101
    port: 2404
    common_address: 1
    point_file_path: points/iec104.csv  # 可以使用同一个CSV文件

  # 第三个IEC104设备(或同一设备的另一个CA)
  - name: iec104_device3
    type: iec104
    host: 192.168.1.100  # 可以是同一个IP
    port: 2404
    common_address: 2  # 不同的CA
    point_file_path: points/iec104.csv
```

**关键点**:
- 每个驱动实例必须有唯一的 `name`
- 可以连接到不同的IP地址(不同设备)
- 可以连接到同一个IP但不同的CA(同一设备的不同公共地址)
- 可以使用同一个CSV文件,通过CA和IOA区分不同设备的测点
- 测点ID格式: `<driver_name>/iec104/<Name>`

### Modbus多驱动实例

当需要连接多个Modbus从站时,可以配置多个驱动实例:

```yaml
drivers:
  # 第一个Modbus从站
  - name: modbus_slave1
    type: modbus
    mode: tcp
    host: 192.168.1.200
    port: 502
    slave_id: 1
    point_file_path: points/modbus.csv

  # 第二个Modbus从站
  - name: modbus_slave2
    type: modbus
    mode: tcp
    host: 192.168.1.201
    port: 502
    slave_id: 2
    point_file_path: points/modbus.csv  # 可以使用同一个CSV文件

  # 第三个Modbus从站(或同一设备的另一个从站ID)
  - name: modbus_slave3
    type: modbus
    mode: tcp
    host: 192.168.1.200  # 可以是同一个IP
    port: 502
    slave_id: 3  # 不同的从站ID
    point_file_path: points/modbus.csv
```

**关键点**:
- 每个驱动实例必须有唯一的 `name`
- 可以连接到不同的IP地址(不同设备)
- 可以连接到同一个IP但不同的slave_id(同一设备的不同从站)
- 可以使用同一个CSV文件,通过Address区分不同设备的测点
- 测点ID格式: `<driver_name>/modbus/<Name>`

## CSV点表文件说明

### IEC104点表文件 (points/iec104.csv)

详见CSV文件中的注释说明。

**关键点**:
- 多个驱动实例可以使用同一个CSV文件
- 通过CA和IOA的唯一组合来区分不同设备的测点
- 如果多个设备有相同的CA+IOA,需要在CSV中为每个设备分别配置,或者使用不同的CSV文件

### Modbus点表文件 (points/modbus.csv)

详见CSV文件中的注释说明。

**关键点**:
- 多个驱动实例可以使用同一个CSV文件
- 通过Address的唯一组合来区分不同设备的测点
- 如果多个设备有相同的Address,需要在CSV中为每个设备分别配置,或者使用不同的CSV文件

## 配置文件最佳实践

1. **使用CSV文件管理点表**:
   - 对于大量测点,推荐使用CSV文件而不是在YAML中直接配置
   - CSV文件更易于编辑和维护
   - CSV文件可以被版本控制

2. **多设备共享CSV文件**:
   - 如果设备的CA/IOA或Address不冲突,可以共享同一个CSV文件
   - 如果有冲突,建议为每个设备使用单独的CSV文件
   - CSV文件命名建议: `points/<driver_name>.csv`

3. **驱动命名规范**:
   - 使用有意义的名称,如 `iec104_substation1`, `modbus_plc1`
   - 避免使用中文或特殊字符
   - 名称在所有驱动实例中必须唯一

4. **系统测点**:
   - 启用系统测点可以监控驱动状态
   - 系统测点ID格式: `$<driver_name>/status`, `$<driver_name>/packet_loss_rate` 等
   - 建议在生产环境中启用

5. **日志级别**:
   - 开发调试时使用 `debug` 级别
   - 生产环境使用 `info` 或 `warn` 级别
   - 避免在生产环境使用 `debug` 级别,会影响性能
