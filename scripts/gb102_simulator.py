#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
GB102 协议模拟器 (DL/T 645 变种)
用于测试 Gateway-go GB102 驱动

基于实际报文分析实现：
- 链路复位: 10 40 04 00 44 16 -> 10 20 04 00 24 16
- 请求一级数据: 10 7A ... -> E5 确认
- 电能量召唤: 68 15 15 68 53 ... (类型 0x78)
- 电能量响应: 类型 0x02，BCD码 + CP56Time2a

使用方式:
    python gb102_simulator.py COM3
    python gb102_simulator.py COM3 -a 4 -b 9600

依赖:
    pip install pyserial
"""

import serial
import struct
import time
import random
import argparse
from datetime import datetime
from typing import Optional, List


class GB102Simulator:
    """GB102 协议从站模拟器"""

    # 协议常量
    START_FIXED = 0x10
    START_VARIABLE = 0x68
    END_BYTE = 0x16
    ACK_BYTE = 0xE5

    # 控制域
    CTRL_RESET_LINK = 0x40
    CTRL_REQ_CLASS1 = 0x7A
    CTRL_CONFIRM = 0x53
    CTRL_LINK_OK = 0x20
    CTRL_RESPONSE = 0x08

    # ASDU 类型
    TYPE_ENERGY_DATA = 0x02
    TYPE_ENERGY_QUERY = 0x78

    # 传输原因
    COT_ACTIVATION = 6
    COT_RESPONSE = 5

    def __init__(self, port: str, address: int = 0x0004, baudrate: int = 9600):
        """
        初始化模拟器

        Args:
            port: 串口名称，如 'COM3' 或 '/dev/ttyUSB0'
            address: 从站地址 (默认 0x0004，即报文中的 04 00)
            baudrate: 波特率 (默认 9600)
        """
        self.port = port
        self.address = address
        self.baudrate = baudrate
        self.serial: Optional[serial.Serial] = None

        # 电能量数据缓存（模拟电能表数据）
        self.energy_data = self._generate_initial_energy_data()

        # 运行状态
        self.running = False

    def _generate_initial_energy_data(self) -> dict:
        """生成初始电能量数据"""
        return {
            # record_addr 0x0B = 日冻结电能量
            # channel 1-8: 正向有功 (费率1-4) + 反向有功 (费率1-4)
            # channel 9-16: 其他费率
            # channel 17-24: 组合有功等
            i: self._generate_bcd_value(random.uniform(1000, 99999))
            for i in range(1, 25)
        }

    def _generate_bcd_value(self, value: float) -> bytes:
        r"""
        将浮点数转换为 BCD 码（4字节，高字节在前）
        例如: 12345.67 -> b'\x12\x34\x56\x7x'
        """
        # 乘以 100 转为整数（假设分辨率 0.01）
        int_value = int(value * 100)

        # 转换为 8 位数字的 BCD
        bcd_bytes = []
        for _ in range(4):  # 从高到低，4个字节
            nibble_low = int_value % 10
            int_value //= 10
            nibble_high = int_value % 10
            int_value //= 10
            bcd_bytes.insert(0, (nibble_high << 4) | nibble_low)

        return bytes(bcd_bytes)

    def _encode_cp56time2a(self, dt: Optional[datetime] = None) -> bytes:
        """
        编码 CP56Time2a 时标（7字节）

        格式:
        - 字节0-1: 毫秒（低字节在前）
        - 字节2: 分钟 (bit0-5) + IV (bit7)
        - 字节3: 小时 (bit0-4)
        - 字节4: 日 (bit0-4)
        - 字节5: 月 (bit0-3)
        - 字节6: 年 (bit0-6, 2000年基准)
        """
        if dt is None:
            dt = datetime.now()

        data = bytearray(7)

        # 毫秒
        msec = dt.second * 1000 + dt.microsecond // 1000
        data[0] = msec & 0xFF
        data[1] = (msec >> 8) & 0xFF

        # 分钟
        data[2] = dt.minute & 0x3F

        # 小时
        data[3] = dt.hour & 0x1F

        # 日
        data[4] = dt.day & 0x1F

        # 月
        data[5] = dt.month & 0x0F

        # 年（2000年基准，取低7位）
        data[6] = (dt.year - 2000) & 0x7F

        return bytes(data)

    def _calc_cs(self, data: bytes) -> int:
        """计算算术和（低字节）"""
        return sum(data) & 0xFF

    def open(self) -> bool:
        """打开串口"""
        try:
            self.serial = serial.Serial(
                port=self.port,
                baudrate=self.baudrate,
                bytesize=serial.EIGHTBITS,
                parity=serial.PARITY_EVEN,
                stopbits=serial.STOPBITS_ONE,
                timeout=0.5
            )
            print(f"[*] 串口已打开: {self.port} @ {self.baudrate}bps")
            print(f"[*] 从站地址: 0x{self.address:04X}")
            return True
        except serial.SerialException as e:
            print(f"[!] 打开串口失败: {e}")
            return False

    def close(self):
        """关闭串口"""
        if self.serial and self.serial.is_open:
            self.serial.close()
            print("[*] 串口已关闭")

    def run(self):
        """运行主循环"""
        if not self.open():
            return

        self.running = True
        print("[*] GB102 模拟器运行中，按 Ctrl+C 退出...\n")

        try:
            while self.running:
                self._process_frame()
        except KeyboardInterrupt:
            print("\n[*] 收到退出信号")
        finally:
            self.close()

    def _process_frame(self):
        """处理接收到的帧"""
        # 读取启动字节
        start_byte = self._read_byte()
        if start_byte is None:
            return

        if start_byte == self.START_FIXED:
            self._handle_fixed_frame()
        elif start_byte == self.START_VARIABLE:
            self._handle_variable_frame()
        elif start_byte == self.ACK_BYTE:
            print("[*] 收到主站确认 (E5)")

    def _read_byte(self) -> Optional[int]:
        """读取一个字节"""
        if not self.serial or not self.serial.is_open:
            return None
        try:
            data = self.serial.read(1)
            if len(data) == 0:
                return None
            return data[0]
        except serial.SerialException:
            return None

    def _read_bytes(self, count: int) -> Optional[bytes]:
        """读取多个字节"""
        if not self.serial or not self.serial.is_open:
            return None
        try:
            data = self.serial.read(count)
            if len(data) < count:
                return None
            return data
        except serial.SerialException:
            return None

    def _send_bytes(self, data: bytes):
        """发送字节"""
        if self.serial and self.serial.is_open:
            self.serial.write(data)
            hex_str = ' '.join(f'{b:02X}' for b in data)
            print(f"[*] 发送: {hex_str}")

    def _handle_fixed_frame(self):
        """处理固定长度帧"""
        # 读取剩余部分: C + A_LO + A_HI + CS + END
        frame = self._read_bytes(5)
        if frame is None or len(frame) < 5:
            return

        control = frame[0]
        addr_lo = frame[1]
        addr_hi = frame[2]
        cs_received = frame[3]
        end_byte = frame[4]

        # 验证结束字节
        if end_byte != self.END_BYTE:
            print(f"[!] 固定帧结束符错误: 0x{end_byte:02X}")
            return

        # 验证地址
        addr = (addr_hi << 8) | addr_lo
        if addr != self.address:
            print(f"[!] 地址不匹配: 收到 0x{addr:04X}, 期望 0x{self.address:04X}")
            return

        # 验证 CS（固定帧: C + A_LO + A_HI 的算术和）
        cs_expected = self._calc_cs(frame[0:3])  # 只取 C + A_LO + A_HI
        if cs_received != cs_expected:
            print(f"[!] 校验和错误: 收到 0x{cs_received:02X}, 期望 0x{cs_expected:02X}")
            return

        # 处理不同控制域
        if control == self.CTRL_RESET_LINK:
            print("[*] 收到链路复位命令")
            self._send_reset_confirm()

        elif control == self.CTRL_REQ_CLASS1:
            print("[*] 收到一级数据请求")
            # 检查是否有待发送的电能量数据
            if self._should_send_energy_data():
                self._send_energy_data_frame()
            else:
                self._send_ack()

    def _send_reset_confirm(self):
        """发送链路复位确认"""
        # 固定帧: 10 20 04 00 24 16
        # CS = 0x20 + 0x04 + 0x00 = 0x24
        addr_lo = self.address & 0xFF
        addr_hi = (self.address >> 8) & 0xFF
        cs = self._calc_cs([self.CTRL_LINK_OK, addr_lo, addr_hi])

        frame = bytes([
            self.START_FIXED,
            self.CTRL_LINK_OK,   # 0x20
            addr_lo,             # A_LO = 0x04
            addr_hi,             # A_HI = 0x00
            cs,                   # CS = 0x24
            self.END_BYTE
        ])
        self._send_bytes(frame)
        print("[*] 发送链路复位确认")

    def _send_ack(self):
        """发送单字节确认"""
        self._send_bytes(bytes([self.ACK_BYTE]))
        print("[*] 发送确认 (E5)")

    def _should_send_energy_data(self) -> bool:
        """判断是否应该发送电能量数据（模拟随机发送）"""
        return random.random() < 0.3  # 30% 概率发送数据

    def _handle_variable_frame(self):
        """处理可变长度帧"""
        # 读取: L + L + 68 + C + A_LO + A_HI + ASDU + CS + END
        header = self._read_bytes(3)
        if header is None or len(header) < 3:
            return

        # 验证 L 重复
        if header[0] != header[1]:
            print(f"[!] L 长度不匹配: {header[0]} != {header[1]}")
            return

        # 验证第二个启动字节
        if header[2] != self.START_VARIABLE:
            print(f"[!] 第二个启动字节错误: 0x{header[2]:02X}")
            return

        length = header[0]

        # 读取剩余数据: C + A_LO + A_HI + ASDU + CS + END
        remaining = self._read_bytes(length + 2)
        if remaining is None or len(remaining) < length + 2:
            return

        control = remaining[0]
        addr_lo = remaining[1]
        addr_hi = remaining[2]
        asdu = remaining[3:length]
        cs_received = remaining[length]
        end_byte = remaining[length + 1]

        # 验证地址
        addr = (addr_hi << 8) | addr_lo
        if addr != self.address:
            print(f"[!] 地址不匹配: 0x{addr:04X}")
            return

        # 验证结束字节
        if end_byte != self.END_BYTE:
            print(f"[!] 可变帧结束符错误: 0x{end_byte:02X}")
            return

        # 验证 CS
        cs_expected = self._calc_cs(remaining[0:length])
        if cs_received != cs_expected:
            print(f"[!] 校验和错误")
            return

        # 确认收到
        self._send_ack()

        # 解析 ASDU
        if len(asdu) > 0:
            type_id = asdu[0]
            if type_id == self.TYPE_ENERGY_QUERY:
                print("[*] 收到电能量召唤命令")
                self._handle_energy_query(asdu)

    def _handle_energy_query(self, asdu: bytes):
        """处理电能量召唤"""
        if len(asdu) < 15:
            return

        # 解析召唤参数
        record_addr = asdu[5] if len(asdu) > 5 else 0
        start_ioa = asdu[6] if len(asdu) > 6 else 0
        end_ioa = asdu[7] if len(asdu) > 7 else 0

        print(f"    记录地址: 0x{record_addr:02X}, IOA: {start_ioa} - {end_ioa}")

        # 发送电能量数据
        self._send_energy_data_frame(record_addr, start_ioa, end_ioa)

    def _send_energy_data_frame(self, record_addr: int = 0x0B,
                                 start_ioa: int = 1, end_ioa: int = 8):
        """发送电能量数据帧（限制为前8个通道避免帧太长）"""
        # 更新电能量数据（模拟递增）
        self._update_energy_data()

        # 构建 ASDU
        asdu = bytearray()
        asdu.append(self.TYPE_ENERGY_DATA)          # 类型标识
        asdu.append(end_ioa - start_ioa + 1)       # VSQ = 信息对象数量（限制为8个）
        asdu.append(self.COT_RESPONSE)             # 传输原因 = 响应
        asdu.append(0x00)                          # 源发地址
        asdu.append(self.address & 0xFF)           # 公共地址
        asdu.append(record_addr)                   # 记录地址

        # 添加时间信息到 ASDU 末尾（所有数据共用）
        timestamp = self._encode_cp56time2a()

        # 添加每个通道的数据（限制为 8 个通道，每通道 12 字节）
        max_channels = min(8, end_ioa - start_ioa + 1)
        for ch in range(start_ioa, start_ioa + max_channels):
            asdu.append(ch)                         # IOA = 通道号
            asdu.extend(self.energy_data.get(ch, self._generate_bcd_value(0)))  # BCD 值
            asdu.extend(timestamp)                 # CP56Time2a 时标

        # 构建可变帧
        # FT1.2 格式: 68H | L | L | 68H | C | A_LO | A_HI | ASDU | CS | 16H
        # L = C(1) + A(2) + ASDU = 3 + ASDU长度
        asdu_len = len(asdu)
        l_field = 3 + asdu_len  # C(1) + A(2) + ASDU

        frame = bytearray()
        frame.append(self.START_VARIABLE)
        frame.append(l_field)        # L 字段
        frame.append(l_field)         # L 重复
        frame.append(self.START_VARIABLE)
        frame.append(self.CTRL_RESPONSE)           # 控制域
        frame.append(self.address & 0xFF)          # A_LO
        frame.append((self.address >> 8) & 0xFF)  # A_HI
        frame.extend(asdu)
        frame.append(self._calc_cs(frame[4:]))     # CS = C + A + ASDU
        frame.append(self.END_BYTE)

        self._send_bytes(bytes(frame))
        print(f"[*] 发送电能量数据: {max_channels} 个通道")

    def _update_energy_data(self):
        """更新电能量数据（模拟随机递增）"""
        for ch in list(self.energy_data.keys()):
            # 随机增加一点电量
            current_value = self._bcd_to_float(self.energy_data[ch])
            new_value = current_value + random.uniform(0.01, 0.5)
            self.energy_data[ch] = self._generate_bcd_value(new_value)

    def _bcd_to_float(self, bcd: bytes) -> float:
        """将 BCD 码转换为浮点数"""
        result = 0
        for b in bcd:
            hi = (b >> 4) & 0x0F
            lo = b & 0x0F
            result = result * 100 + hi * 10 + lo
        return result / 100.0  # 除以 100 恢复原始值

    def stop(self):
        """停止运行"""
        self.running = False


def main():
    parser = argparse.ArgumentParser(
        description="GB102 协议模拟器 (DL/T 645 变种)",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
示例:
    python gb102_simulator.py COM3              # 默认参数
    python gb102_simulator.py COM3 -a 4 -b 9600  # 指定地址和波特率
    python gb102_simulator.py /dev/ttyUSB0       # Linux 串口

注意: 需要先使用 com0com 或 VSPE 创建虚拟串口对，
      一端运行模拟器，一端运行 Gateway。
        """
    )

    parser.add_argument('port', help='串口名称 (如 COM3, /dev/ttyUSB0)')
    parser.add_argument('-a', '--address', type=lambda x: int(x, 0), default=0x0004,
                       help='从站地址 (默认 0x0004)')
    parser.add_argument('-b', '--baudrate', type=int, default=9600,
                       help='波特率 (默认 9600)')

    args = parser.parse_args()

    simulator = GB102Simulator(
        port=args.port,
        address=args.address,
        baudrate=args.baudrate
    )
    simulator.run()


if __name__ == "__main__":
    main()