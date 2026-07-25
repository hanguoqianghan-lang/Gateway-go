# GB102 模拟器使用指南

## 环境准备

### 1. 安装 Python 依赖

```bash
pip install pyserial
```

或：

```bash
cd D:\Claude\Gateway-go\scripts
pip install -r requirements.txt
```

### 2. 安装虚拟串口工具

需要 **com0com** 或 **VSPE** 创建虚拟串口对。

#### Windows 10/11 方案：com0com

1. 下载 com0com: https://sourceforge.net/projects/com0com/
2. 安装时选择 "Install COM extensions"
3. 创建虚拟串口对：
   ```cmd
   > setupc
   > list
   > install  portname=COM3 portname=COM4
   > list
   ```

#### Windows 方案：VSPE (Virtual Serial Port Emulator)

1. 下载 VSPE: https://www.eterlogic.com/Products.VSPE.html
2. 创建串口对：
   - 选择 "Device" → "Creating"
   - 类型选择 "Connector"
   - 端口: COM5 → COM6

## 测试步骤

### 步骤 1: 创建虚拟串口对

假设创建了 **COM3 ↔ COM4** 对：
- COM3: 运行模拟器
- COM4: 运行 Gateway

### 步骤 2: 启动 GB102 模拟器

```bash
cd D:\Claude\Gateway-go\scripts
python gb102_simulator.py COM3
```

输出示例：
```
[*] 串口已打开: COM3 @ 9600bps
[*] 从站地址: 0x0004
[*] GB102 模拟器运行中，按 Ctrl+C 退出...
```

### 步骤 3: 启动 Gateway

编辑配置文件 `config/config_gb102_test.yaml`，设置串口为 COM4：

```yaml
gb102:
  serial_port: "COM4"  # 虚拟串口对的另一端
```

然后运行：

```bash
cd D:\Claude\Gateway-go
go build -o gateway.exe ./cmd/gateway/
./gateway.exe -config ./config/config_gb102_test.yaml
```

### 步骤 4: 观察日志

模拟器端会显示：
```
[*] 收到链路复位命令
[*] 发送链路复位确认
[*] 收到一级数据请求
[*] 发送电能量数据: 24 个通道
```

Gateway 端会显示采集到的数据。

## 协议流程

模拟器实现的协议交互：

```
Gateway (主站)                          模拟器 (从站)
    |                                        |
    |-------- 10 40 04 00 44 16 ------------>|
    |<------- 10 20 04 00 24 16 -------------|
    |  (链路复位确认)                         |
    |                                        |
    |-------- 10 7A 04 00 7E 16 ------------>|
    |<------- E5 (确认) -----------------------|
    |                                        |
    |-------- 68 15 15 68 53 ... ----------->|
    |<------- E5 (确认) -----------------------|
    |  (电能量召唤)                           |
    |                                        |
    |-------- 10 7A 04 00 7E 16 ------------>|
    |<------- 68 b6 b6 68 28 04 00 02 ... ---|
    |  (电能量数据响应)                        |
```

## 常见问题

### Q: 串口打开失败 "Access is denied"

另一个程序正在使用该串口。关闭占用串口的程序。

### Q: 模拟器没有收到任何数据

1. 检查串口是否正确（COM3 vs COM4）
2. 检查波特率是否匹配（9600）
3. 检查校验位是否为 Even

### Q: 如何模拟不同地址的电能表?

```bash
python gb102_simulator.py COM3 -a 0x1234
```

## 高级用法

### 修改电能量初始值

编辑 `gb102_simulator.py` 中的 `_generate_initial_energy_data` 方法：

```python
def _generate_initial_energy_data(self) -> dict:
    return {
        1: self._generate_bcd_value(12345.67),  # 通道1 = 12345.67
        2: self._generate_bcd_value(9876.54),  # 通道2 = 9876.54
        # ...
    }
```

### 调整数据发送频率

修改 `_should_send_energy_data` 方法中的概率：

```python
def _should_send_energy_data(self) -> bool:
    return random.random() < 0.8  # 80% 概率发送数据
```