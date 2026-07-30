#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
GB/T 26875.3-2011 模拟器
模拟传输装置连接网关，发送各类型上行报文
用于网关驱动开发调试和集成测试
"""

import socket
import struct
import time
import threading
import argparse
import sys
from datetime import datetime
from typing import List, Tuple, Optional

# ================================
# 协议常量定义（与 GB/T 26875.3-2011 标准对齐）
# ================================
FRAME_START_1 = 0x40
FRAME_START_2 = 0x40
FRAME_END_1 = 0x23
FRAME_END_2 = 0x23

CONTROL_UNIT_LEN = 25  # 控制单元长度（不含启动符、校验和、结束符）
MaxADULen = 1024

# 命令字（Table 2 of GB/T 26875.3）
CMD_RESERVED = 0x00
CMD_CONTROL  = 0x01  # 控制命令（时钟同步）
CMD_SEND_DATA = 0x02 # 发送数据（上传报警/状态信息）
CMD_CONFIRM  = 0x03  # 确认
CMD_REQUEST  = 0x04  # 请求（查询操作）
CMD_REPLY    = 0x05  # 应答（返回查询信息）
CMD_DENY     = 0x06  # 否认

# 上行类型标识（传输装置 -> 监控中心）
# 建筑消防设施
TYPE_UPLOAD_SYSTEM_STATUS              = 1   # 上传建筑消防设施系统状态
TYPE_UPLOAD_COMPONENT_STATUS           = 2   # 上传建筑消防设施部件运行状态
TYPE_UPLOAD_COMPONENT_ANALOG           = 3   # 上传建筑消防设施部件模拟量值
TYPE_UPLOAD_OPERATION_INFO             = 4   # 上传建筑消防设施操作信息
TYPE_UPLOAD_SW_VERSION                 = 5   # 上传建筑消防设施软件版本
TYPE_UPLOAD_SYS_CONFIG                 = 6   # 上传建筑消防设施系统配置情况
TYPE_UPLOAD_COMPONENT_CONFIG           = 7   # 上传建筑消防设施部件配置情况
TYPE_UPLOAD_SYSTEM_TIME                = 8   # 上传建筑消防设施系统时间
# 用户信息传输装置
TYPE_UPLOAD_TRANSMISSION_DEVICE_STATUS = 21  # 上传用户信息传输装置运行状态
TYPE_UPLOAD_TRANSMISSION_OP_INFO       = 24  # 上传用户信息传输装置操作信息
TYPE_UPLOAD_TRANSMISSION_SW_VER        = 25  # 上传用户信息传输装置软件版本
TYPE_UPLOAD_TRANSMISSION_CONFIG        = 26  # 上传用户信息传输装置配置情况
TYPE_UPLOAD_TRANSMISSION_TIME          = 28  # 上传用户信息传输装置系统时间

# 下行类型标识（监控中心 -> 传输装置）
TYPE_READ_FIRE_SYSTEM_STATUS          = 61 # 读建筑消防设施系统状态
TYPE_READ_FIRE_COMPONENT_STATUS       = 62 # 读建筑消防设施部件运行状态
TYPE_READ_FIRE_COMPONENT_ANALOG       = 63 # 读建筑消防设施部件模拟量值
TYPE_READ_FIRE_OPERATION_INFO         = 64 # 读建筑消防设施操作信息
TYPE_READ_FIRE_SW_VERSION             = 65 # 读建筑消防设施软件版本
TYPE_READ_FIRE_SYSTEM_CONFIG          = 66 # 读建筑消防设施系统配置情况
TYPE_READ_FIRE_COMPONENT_CONFIG       = 67 # 读建筑消防设施部件配置情况
TYPE_READ_FIRE_SYSTEM_TIME            = 68 # 读建筑消防设施系统时间
TYPE_READ_TRANSMISSION_DEVICE_STATUS  = 81 # 读用户信息传输装置运行状态
TYPE_READ_TRANSMISSION_OPERATION      = 84 # 读用户信息传输装置操作信息记录
TYPE_READ_TRANSMISSION_SW_VER         = 85 # 读用户信息传输装置软件版本
TYPE_READ_TRANSMISSION_CONFIG         = 86 # 读用户信息传输装置配置情况
TYPE_READ_TRANSMISSION_TIME           = 88 # 读用户信息传输装置系统时间
TYPE_INITIALIZE_TRANSMISSION_DEVICE   = 89 # 初始化用户信息传输装置
TYPE_SYNC_CLOCK                       = 90 # 同步用户信息传输装置时钟
TYPE_CHECK_POST                       = 91 # 查岗命令

# 系统地址
SYS_ADDR_IDENTIFICATION = 1          # 系统标识
SYS_ADDR_PARAMETER = 2               # 系统参数
SYS_ADDR_STATUS = 3                  # 系统状态
SYS_ADDR_FAULT = 4                   # 系统故障
SYS_ADDR_OPERATION = 5               # 操作信息
SYS_ADDR_SW_VERSION = 6              # 软件版本
SYS_ADDR_TX_DEVICE_STATUS = 7        # 传输装置状态
SYS_ADDR_HISTORY_INDEX = 8           # 历史数据索引
SYS_ADDR_SOE_INDEX = 9               # SOE索引
SYS_ADDR_PATROL_INDEX = 10           # 巡检索引

# 部件类型示例
COMP_TYPE_GENERIC = 30               # 通用部件
COMP_TYPE_DETECTOR = 1               # 探测器
COMP_TYPE_MODULE = 2                 # 模块
COMP_TYPE_CONTROLLER = 3             # 控制器

# 模拟量类型
ANALOG_TYPE_TEMP = 1                 # 温度
ANALOG_TYPE_HUMIDITY = 2             # 湿度
ANALOG_TYPE_VOLTAGE = 3              # 电压
ANALOG_TYPE_CURRENT = 4              # 电流
ANALOG_TYPE_PRESSURE = 5             # 压力
ANALOG_TYPE_FLOW = 6                 # 流量
ANALOG_TYPE_CONCENTRATION = 7        # 浓度
ANALOG_TYPE_FREQUENCY = 8            # 频率
ANALOG_TYPE_POWER = 9                # 功率
ANALOG_TYPE_ENERGY = 10              # 能量
ANALOG_TYPE_OTHER = 255              # 其他

# ================================
# 工具函数
# ================================

def bcd_encode(value: int, length: int) -> bytes:
    """整数编码为 BCD 字节序列（低字节在前，符合协议小端存储）"""
    result = bytearray(length)
    for i in range(length):
        result[i] = ((value % 10) << 4) | (value // 10 % 10)
        value //= 100
    return bytes(result)


def bcd_decode(data: bytes) -> int:
    """BCD 字节序列解码为整数"""
    value = 0
    for i in reversed(range(len(data))):
        value = value * 100 + ((data[i] >> 4) * 10 + (data[i] & 0x0F))
    return value


def calc_checksum(data: bytes) -> int:
    """计算校验和（算术和 mod 256）"""
    return sum(data) & 0xFF


def build_time_label(dt: Optional[datetime] = None) -> bytes:
    """构建时间标签（6字节 BCD：秒、分、时、日、月、年-2000）"""
    if dt is None:
        dt = datetime.now()
    return bytes([
        ((dt.second // 10) << 4) | (dt.second % 10),   # 秒
        ((dt.minute // 10) << 4) | (dt.minute % 10),   # 分
        ((dt.hour // 10) << 4) | (dt.hour % 10),       # 时
        ((dt.day // 10) << 4) | (dt.day % 10),         # 日
        ((dt.month // 10) << 4) | (dt.month % 10),     # 月
        ((dt.year - 2000) // 10) << 4 | ((dt.year - 2000) % 10)  # 年-2000
    ])


def parse_addr_string(addr_str: str) -> bytes:
    """解析 6字节地址字符串（如 '800D00000000'）为字节序列（低字节在前，符合协议小端存储）"""
    clean = addr_str.replace('-', '').replace(':', '').upper()
    if len(clean) != 12:
        raise ValueError(f"地址长度必须为12个十六进制字符: {addr_str}")
    # 协议要求：地址在帧中以小端序传输（低字节在前）
    # 字符串 "800D00000000" -> 字节 [0x80, 0x0D, 0x00, 0x00, 0x00, 0x00]
    # 存储/发送时: [0x00, 0x00, 0x00, 0x00, 0x0D, 0x80] (反转)
    return bytes.fromhex(clean)[::-1]


def parse_comp_addr_string(addr_str: str) -> bytes:
    """解析 4字节部件地址字符串（如 '01000100'）为字节序列（低字节在前）"""
    clean = addr_str.replace('-', '').replace(':', '').upper()
    if len(clean) != 8:
        raise ValueError(f"部件地址长度必须为8个十六进制字符: {addr_str}")
    # 协议要求：部件地址在帧中以小端序传输
    return bytes.fromhex(clean)[::-1]


def format_addr(addr: bytes) -> str:
    """格式化地址为可读字符串（高字节在前显示）"""
    # 与 Go 端 StringAddr 保持一致：显示时反转
    return addr[::-1].hex().upper()


def format_comp_addr(addr: bytes) -> str:
    """格式化部件地址为可读字符串"""
    # 与 Go 端 StringComponentAddr4 保持一致：显示时反转
    return addr[::-1].hex().upper()


# ================================
# 帧构建器
# ================================

class FrameBuilder:
    """GB/T 26875.3 帧构建器"""

    def __init__(self, src_addr: bytes, dst_addr: bytes,
                 version: int = 1, user_version: int = 1):
        """
        初始化帧构建器
        :param src_addr: 源地址（6字节，低字节在前）
        :param dst_addr: 目的地址（6字节，低字节在前）
        :param version: 主版本号
        :param user_version: 用户版本号
        """
        self.src_addr = src_addr
        self.dst_addr = dst_addr
        self.version = version
        self.user_version = user_version
        self.seq_no = 0

    def _next_seq(self) -> int:
        self.seq_no = (self.seq_no + 1) & 0xFFFF
        return self.seq_no

    def build_frame(self, cmd: int, adu: bytes = b'') -> bytes:
        """构建完整帧"""
        seq_no = self._next_seq()
        time_label = build_time_label()

        # 控制单元（25字节）
        # 业务流水号(2) + 版本号(2) + 时间标签(6) + 源地址(6) + 目的地址(6) + ADU长度(2) + 命令字(1)
        control_unit = bytearray(CONTROL_UNIT_LEN)
        offset = 0

        # 业务流水号（小端）
        control_unit[offset:offset+2] = struct.pack('<H', seq_no)
        offset += 2

        # 协议版本号（2字节：主版本 + 用户版本）
        control_unit[offset] = self.version
        control_unit[offset+1] = self.user_version
        offset += 2

        # 时间标签（6字节）
        control_unit[offset:offset+6] = time_label
        offset += 6

        # 源地址（6字节）
        control_unit[offset:offset+6] = self.src_addr
        offset += 6

        # 目的地址（6字节）
        control_unit[offset:offset+6] = self.dst_addr
        offset += 6

        # ADU长度（小端，2字节）
        adu_len = len(adu)
        control_unit[offset:offset+2] = struct.pack('<H', adu_len)
        offset += 2

        # 命令字（1字节）
        control_unit[offset] = cmd
        offset += 1

        # 计算校验和（控制单元 + ADU）
        checksum_data = bytes(control_unit) + adu
        checksum = calc_checksum(checksum_data)

        # 组装完整帧：启动符(2) + 控制单元(25) + ADU + 校验和(1) + 结束符(2)
        frame = bytearray()
        frame.append(FRAME_START_1)
        frame.append(FRAME_START_2)
        frame.extend(control_unit)
        if adu_len > 0:
            frame.extend(adu)
        frame.append(checksum)
        frame.append(FRAME_END_1)
        frame.append(FRAME_END_2)

        return bytes(frame)

    def build_upload(self, msg_type: int, adu_objects: bytes) -> bytes:
        """构建上行数据帧（命令字=2，上行数据）"""
        # ADU = 类型标识(1) + 对象个数(1) + 对象数据
        adu = bytearray()
        adu.append(msg_type)
        # 对象个数由具体构建函数决定，这里假设已包含在 adu_objects 中
        adu.extend(adu_objects)
        return self.build_frame(CMD_SEND_DATA, bytes(adu))  # 发送数据帧用于上行数据

    def build_ack(self) -> bytes:
        """构建确认帧"""
        return self.build_frame(CMD_CONFIRM)

    def build_deny(self) -> bytes:
        """构建否认帧"""
        return self.build_frame(CMD_DENY)


# ================================
# ADU 对象构建函数
# ================================

def build_system_status_obj(sys_addr: int, status_data: int, include_time: bool = False) -> bytes:
    """构建系统状态对象（4字节或10字节含时间）"""
    # 系统地址(1) + 状态数据(3) + 可选时间标签(6)
    obj = bytearray()
    obj.append(sys_addr & 0xFF)
    # 状态数据 3 字节（小端存储 24 位值）
    obj.extend(struct.pack('<I', status_data & 0xFFFFFF)[:3])
    if include_time:
        obj.extend(build_time_label())
    return bytes(obj)


def build_component_status_obj(sys_addr: int, comp_type: int, comp_addr: bytes,
                                run_status: int, fault_status: int = 0,
                                include_time: bool = False) -> bytes:
    """构建部件运行状态对象（40字节或46字节含时间）

    协议格式（8.2.1.2）：
    系统类型(1) + 系统地址(1) + 部件类型(1) + 部件地址(4) + 运行状态(2) + 描述/故障状态(31)
    """
    obj = bytearray()
    # 系统类型：传输装置=1（参考协议表4）
    obj.append(1)
    # 系统地址
    obj.append(sys_addr & 0xFF)
    # 部件类型
    obj.append(comp_type & 0xFF)
    # 部件地址（4字节，低字节在前）
    obj.extend(comp_addr[:4])
    # 运行状态（2字节小端）
    obj.extend(struct.pack('<H', run_status & 0xFFFF))
    # 故障状态（2字节小端）
    obj.extend(struct.pack('<H', fault_status & 0xFFFF))
    # 预留/扩展字节补齐到 40 字节（当前已用 1+1+1+4+2+2=11 字节，还需 29 字节）
    while len(obj) < 40:
        obj.append(0)
    if include_time:
        obj.extend(build_time_label())
    return bytes(obj)


def build_component_analog_obj(sys_addr: int, comp_type: int, comp_addr: bytes,
                                analog_type: int, analog_value: int,
                                include_time: bool = False) -> bytes:
    """构建部件模拟量对象（10字节或16字节含时间）

    协议格式（8.2.1.3）：
    系统类型(1) + 系统地址(1) + 部件类型(1) + 部件地址(4) + 模拟量类型(1) + 模拟量值(2)
    """
    obj = bytearray()
    # 系统类型：传输装置=1
    obj.append(1)
    # 系统地址
    obj.append(sys_addr & 0xFF)
    # 部件类型
    obj.append(comp_type & 0xFF)
    # 部件地址（4字节，低字节在前）
    obj.extend(comp_addr[:4])
    # 模拟量类型
    obj.append(analog_type & 0xFF)
    # 模拟量值（2字节有符号整数，小端）
    obj.extend(struct.pack('<h', analog_value & 0xFFFF))
    if include_time:
        obj.extend(build_time_label())
    return bytes(obj)


def build_operation_info_obj(sys_addr: int, operator_id: int, op_code: int,
                              target_addr: bytes, result: int,
                              include_time: bool = False) -> bytes:
    """构建操作信息对象（4字节或10字节含时间）"""
    obj = bytearray()
    obj.append(sys_addr & 0xFF)           # 系统地址
    obj.append(operator_id & 0xFF)        # 操作员编号
    obj.append(op_code & 0xFF)            # 操作码
    obj.append(result & 0xFF)             # 结果
    if include_time:
        obj.extend(build_time_label())
    return bytes(obj)


def build_sw_version_obj(sys_addr: int, version: int) -> bytes:
    """构建软件版本对象（4字节）"""
    obj = bytearray()
    obj.append(sys_addr & 0xFF)
    obj.extend(struct.pack('<I', version & 0xFFFFFF)[:3])
    return bytes(obj)


def build_tx_device_status_obj(sys_addr: int, status: int, signal_strength: int = 0) -> bytes:
    """构建传输装置状态对象"""
    obj = bytearray()
    obj.append(sys_addr & 0xFF)
    obj.append(status & 0xFF)
    obj.extend(struct.pack('<H', signal_strength & 0xFFFF))
    # 补齐
    while len(obj) < 10:
        obj.append(0)
    return bytes(obj)


# ================================
# 模拟器主类
# ================================

class GB26875Simulator:
    """GB/T 26875.3 传输装置模拟器"""

    def __init__(self, host: str, port: int,
                 device_addr: str = "800D00000000",
                 center_addr: str = "000000000000",
                 version: int = 1, user_version: int = 1):
        self.host = host
        self.port = port
        self.device_addr = parse_addr_string(device_addr)  # 传输装置地址（作为源地址）
        self.center_addr = parse_addr_string(center_addr)  # 监控中心地址（作为目的地址）
        self.version = version
        self.user_version = user_version
        self.sock: Optional[socket.socket] = None
        self.running = False
        self.recv_thread: Optional[threading.Thread] = None
        self.frame_builder = FrameBuilder(self.device_addr, self.center_addr, version, user_version)

        # 统计
        self.stats = {
            'sent': 0,
            'received': 0,
            'acks': 0,
            'errors': 0
        }
        self.stats_lock = threading.Lock()

    def connect(self) -> bool:
        """连接到网关"""
        try:
            self.sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
            self.sock.settimeout(5.0)
            self.sock.connect((self.host, self.port))
            self.sock.settimeout(None)  # 阻塞模式
            print(f"[+] 已连接到 {self.host}:{self.port}")
            print(f"    装置地址: {format_addr(self.device_addr)}")
            print(f"    中心地址: {format_addr(self.center_addr)}")
            return True
        except Exception as e:
            print(f"[-] 连接失败: {e}")
            return False

    def disconnect(self):
        """断开连接"""
        self.running = False
        if self.sock:
            try:
                self.sock.close()
            except:
                pass
            self.sock = None
        if self.recv_thread and self.recv_thread.is_alive():
            self.recv_thread.join(timeout=2)
        print("[+] 已断开连接")

    def send_frame(self, frame: bytes) -> bool:
        """发送帧"""
        if not self.sock:
            return False
        try:
            self.sock.sendall(frame)
            with self.stats_lock:
                self.stats['sent'] += 1
            # print(f"[TX] {len(frame)} bytes: {frame.hex().upper()}")
            return True
        except Exception as e:
            print(f"[-] 发送失败: {e}")
            with self.stats_lock:
                self.stats['errors'] += 1
            return False

    def send_ack(self):
        """发送确认帧"""
        frame = self.frame_builder.build_ack()
        self.send_frame(frame)
        with self.stats_lock:
            self.stats['acks'] += 1

    def start_receiving(self):
        """启动接收线程"""
        self.running = True
        self.recv_thread = threading.Thread(target=self._recv_loop, daemon=True)
        self.recv_thread.start()

    def _recv_loop(self):
        """接收循环"""
        buffer = bytearray()
        while self.running and self.sock:
            try:
                data = self.sock.recv(4096)
                if not data:
                    print("[-] 连接已关闭")
                    break
                buffer.extend(data)
                with self.stats_lock:
                    self.stats['received'] += len(data)

                # 简单的帧边界处理：查找 0x40 0x40 ... 0x23 0x23
                while True:
                    start_idx = buffer.find(b'@@')  # 0x40 0x40
                    if start_idx < 0:
                        # 没有找到启动符，保留最后几个字节防止跨包
                        if len(buffer) > 2:
                            buffer = buffer[-2:]
                        break

                    end_idx = buffer.find(b'##', start_idx + 2)  # 0x23 0x23
                    if end_idx < 0:
                        # 帧未完整，等待更多数据
                        if start_idx > 0:
                            buffer = buffer[start_idx:]
                        break

                    # 提取完整帧
                    frame = buffer[start_idx:end_idx + 2]
                    buffer = buffer[end_idx + 2:]

                    self._handle_frame(frame)

            except socket.timeout:
                continue
            except Exception as e:
                if self.running:
                    print(f"[-] 接收错误: {e}")
                break

    def _handle_frame(self, frame: bytes):
        """处理接收到的帧"""
        if len(frame) < 30:  # 最小帧长
            return

        # 解析控制单元
        # 跳过启动符(2)，解析控制单元(25)
        cu = frame[2:27]
        if len(cu) < 25:
            return

        seq_no = struct.unpack('<H', cu[0:2])[0]
        ver = cu[2]
        user_ver = cu[3]
        time_label = cu[4:10]
        src_addr = cu[10:16]
        dst_addr = cu[16:22]
        adu_len = struct.unpack('<H', cu[22:24])[0]
        cmd = cu[24]

        adu = frame[27:27+adu_len] if adu_len > 0 else b''
        checksum = frame[27+adu_len] if len(frame) > 27+adu_len else 0

        # 验证校验和
        calc_cs = calc_checksum(cu + adu)
        if checksum != calc_cs:
            print(f"[!] 校验和错误: 收到={checksum:02X}, 计算={calc_cs:02X}")
            return

        # 打印接收信息
        cmd_names = {
            CMD_CONFIRM: "确认帧",
            CMD_DENY: "否认帧",
            CMD_REQUEST: "请求帧",
            CMD_CONTROL: "控制命令帧",
            CMD_REPLY: "应答帧"
        }
        cmd_name = cmd_names.get(cmd, f"未知(0x{cmd:02X})")
        print(f"[RX] Seq={seq_no}, Cmd={cmd_name}(0x{cmd:02X}), "
              f"Src={format_addr(src_addr)}, Dst={format_addr(dst_addr)}, "
              f"ADU_Len={adu_len}, Time={time_label.hex().upper()}")

        # 处理不同命令
        if cmd == CMD_REQUEST:
            # 下行请求，解析 ADU 类型
            if len(adu) >= 2:
                msg_type = adu[0]
                obj_count = adu[1]
                print(f"    下行请求: 类型={msg_type}, 对象数={obj_count}")
                # 自动回确认
                self.send_ack()

        elif cmd == CMD_CONTROL:
            # 下行控制命令
            if len(adu) >= 2:
                msg_type = adu[0]
                obj_count = adu[1]
                print(f"    下行控制: 类型={msg_type}, 对象数={obj_count}")
                self.send_ack()

        elif cmd == CMD_CONFIRM:
            print(f"    收到确认帧")

        elif cmd == CMD_DENY:
            print(f"    收到否认帧")

    # ================================
    # 发送各类型上行数据的便捷方法
    # ================================

    def send_system_status(self, sys_addr: int = SYS_ADDR_STATUS,
                           status: int = 0x01, include_time: bool = False):
        """发送系统状态上传"""
        obj = build_system_status_obj(sys_addr, status, include_time)
        adu = bytearray()
        adu.append(TYPE_UPLOAD_SYSTEM_STATUS)
        adu.append(1)  # 对象个数
        adu.extend(obj)
        frame = self.frame_builder.build_frame(CMD_SEND_DATA, bytes(adu))
        self.send_frame(frame)
        print(f"[TX] 系统状态上传: SysAddr={sys_addr}, Status=0x{status:06X}")

    def send_component_status(self, sys_addr: int = SYS_ADDR_STATUS,
                               comp_type: int = COMP_TYPE_GENERIC,
                               comp_addr: str = "01000100",
                               run_status: int = 1, fault_status: int = 0,
                               include_time: bool = False):
        """发送部件运行状态上传"""
        comp_addr_bytes = parse_comp_addr_string(comp_addr)
        obj = build_component_status_obj(sys_addr, comp_type, comp_addr_bytes,
                                          run_status, fault_status, include_time)
        adu = bytearray()
        adu.append(TYPE_UPLOAD_COMPONENT_STATUS)
        adu.append(1)
        adu.extend(obj)
        frame = self.frame_builder.build_frame(CMD_SEND_DATA, bytes(adu))
        self.send_frame(frame)
        print(f"[TX] 部件状态上传: CompType={comp_type}, Addr={comp_addr}, "
              f"RunStatus={run_status}, FaultStatus={fault_status}")

    def send_component_analog(self, sys_addr: int = SYS_ADDR_STATUS,
                               comp_type: int = COMP_TYPE_GENERIC,
                               comp_addr: str = "01000100",
                               analog_type: int = ANALOG_TYPE_TEMP,
                               analog_value: int = 250,  # 25.0°C * 10
                               include_time: bool = False):
        """发送部件模拟量上传"""
        comp_addr_bytes = parse_comp_addr_string(comp_addr)
        obj = build_component_analog_obj(sys_addr, comp_type, comp_addr_bytes,
                                          analog_type, analog_value, include_time)
        adu = bytearray()
        adu.append(TYPE_UPLOAD_COMPONENT_ANALOG)
        adu.append(1)
        adu.extend(obj)
        frame = self.frame_builder.build_frame(CMD_SEND_DATA, bytes(adu))
        self.send_frame(frame)
        analog_names = {
            ANALOG_TYPE_TEMP: "温度", ANALOG_TYPE_HUMIDITY: "湿度",
            ANALOG_TYPE_VOLTAGE: "电压", ANALOG_TYPE_CURRENT: "电流",
            ANALOG_TYPE_PRESSURE: "压力"
        }
        name = analog_names.get(analog_type, f"类型{analog_type}")
        print(f"[TX] 模拟量上传: {name}, 值={analog_value}, Addr={comp_addr}")

    def send_operation_info(self, sys_addr: int = SYS_ADDR_OPERATION,
                             operator_id: int = 1, op_code: int = 0x01,
                             target_addr: str = "01000100", result: int = 0,
                             include_time: bool = False):
        """发送操作信息上传"""
        target_addr_bytes = parse_comp_addr_string(target_addr)
        obj = build_operation_info_obj(sys_addr, operator_id, op_code,
                                        target_addr_bytes, result, include_time)
        adu = bytearray()
        adu.append(TYPE_UPLOAD_OPERATION_INFO)
        adu.append(1)
        adu.extend(obj)
        frame = self.frame_builder.build_frame(CMD_SEND_DATA, bytes(adu))
        self.send_frame(frame)
        print(f"[TX] 操作信息上传: OpID={operator_id}, OpCode=0x{op_code:02X}, Result={result}")

    def send_sw_version(self, sys_addr: int = SYS_ADDR_SW_VERSION,
                         version: int = 0x010001):
        """发送软件版本上传"""
        obj = build_sw_version_obj(sys_addr, version)
        adu = bytearray()
        adu.append(TYPE_UPLOAD_SW_VERSION)
        adu.append(1)
        adu.extend(obj)
        frame = self.frame_builder.build_frame(CMD_SEND_DATA, bytes(adu))
        self.send_frame(frame)
        print(f"[TX] 软件版本上传: Version=0x{version:06X}")

    def send_tx_device_status(self, sys_addr: int = SYS_ADDR_TX_DEVICE_STATUS,
                               status: int = 1, signal: int = 85):
        """发送传输装置状态上传"""
        obj = build_tx_device_status_obj(sys_addr, status, signal)
        adu = bytearray()
        adu.append(TYPE_UPLOAD_TRANSMISSION_DEVICE_STATUS)
        adu.append(1)
        adu.extend(obj)
        frame = self.frame_builder.build_frame(CMD_SEND_DATA, bytes(adu))
        self.send_frame(frame)
        print(f"[TX] 传输装置状态上传: Status={status}, Signal={signal}%")

    def send_batch_test(self):
        """批量发送所有类型测试数据"""
        print("\n=== 批量发送测试数据 ===\n")

        # 1. 系统状态
        self.send_system_status(SYS_ADDR_STATUS, 0x01)
        time.sleep(0.2)

        # 2. 部件运行状态
        self.send_component_status(SYS_ADDR_STATUS, COMP_TYPE_GENERIC, "01000100", 1, 0)
        time.sleep(0.2)
        self.send_component_status(SYS_ADDR_STATUS, COMP_TYPE_DETECTOR, "02000100", 1, 0)
        time.sleep(0.2)
        self.send_component_status(SYS_ADDR_FAULT, COMP_TYPE_GENERIC, "01000100", 0, 0x0003)
        time.sleep(0.2)

        # 3. 模拟量 - 温度
        self.send_component_analog(SYS_ADDR_IDENTIFICATION, COMP_TYPE_GENERIC, "01000100",
                                    ANALOG_TYPE_TEMP, 250)  # 25.0°C
        time.sleep(0.2)
        # 模拟量 - 湿度
        self.send_component_analog(SYS_ADDR_IDENTIFICATION, COMP_TYPE_GENERIC, "01000101",
                                    ANALOG_TYPE_HUMIDITY, 600)  # 60.0%
        time.sleep(0.2)
        # 模拟量 - 电压
        self.send_component_analog(SYS_ADDR_IDENTIFICATION, COMP_TYPE_GENERIC, "01000102",
                                    ANALOG_TYPE_VOLTAGE, 2400)  # 24.00V
        time.sleep(0.2)
        # 模拟量 - 电流
        self.send_component_analog(SYS_ADDR_IDENTIFICATION, COMP_TYPE_GENERIC, "01000103",
                                    ANALOG_TYPE_CURRENT, 150)  # 1.50A
        time.sleep(0.2)
        # 模拟量 - 压力
        self.send_component_analog(SYS_ADDR_IDENTIFICATION, COMP_TYPE_GENERIC, "01000104",
                                    ANALOG_TYPE_PRESSURE, 1013)  # 101.3kPa
        time.sleep(0.2)

        # 4. 操作信息
        self.send_operation_info(SYS_ADDR_OPERATION, 1, 0x01, "01000100", 0)
        time.sleep(0.2)
        self.send_operation_info(SYS_ADDR_OPERATION, 2, 0x02, "02000100", 0)
        time.sleep(0.2)

        # 5. 软件版本
        self.send_sw_version(SYS_ADDR_SW_VERSION, 0x010203)
        time.sleep(0.2)

        # 6. 传输装置状态
        self.send_tx_device_status(SYS_ADDR_TX_DEVICE_STATUS, 1, 92)
        time.sleep(0.2)

        print("\n=== 批量发送完成 ===\n")

    def print_stats(self):
        """打印统计信息"""
        with self.stats_lock:
            print(f"\n=== 统计 ===")
            print(f"  发送帧数: {self.stats['sent']}")
            print(f"  接收字节: {self.stats['received']}")
            print(f"  发送确认: {self.stats['acks']}")
            print(f"  错误次数: {self.stats['errors']}")


# ================================
# 命令行入口
# ================================

def main():
    parser = argparse.ArgumentParser(
        description='GB/T 26875.3-2011 传输装置模拟器',
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
示例:
  # 连接并批量发送测试数据
  python gb26875_simulator.py -H 127.0.0.1 -p 5001 --batch

  # 连接并手动交互
  python gb26875_simulator.py -H 127.0.0.1 -p 5001 --interactive

  # 指定装置地址和中心地址
  python gb26875_simulator.py -H 127.0.0.1 -p 5001 \
    --device-addr 800D00000001 --center-addr 000000000000 --batch
        """
    )
    parser.add_argument('-H', '--host', default='127.0.0.1',
                        help='网关 IP 地址 (默认: 127.0.0.1)')
    parser.add_argument('-p', '--port', type=int, default=5001,
                        help='网关端口 (默认: 5001)')
    parser.add_argument('--device-addr', default='800D00000000',
                        help='传输装置地址 6字节HEX (默认: 800D00000000)')
    parser.add_argument('--center-addr', default='000000000000',
                        help='监控中心地址 6字节HEX (默认: 000000000000)')
    parser.add_argument('--version', type=int, default=1,
                        help='协议主版本号 (默认: 1)')
    parser.add_argument('--user-version', type=int, default=1,
                        help='用户版本号 (默认: 1)')
    parser.add_argument('--batch', action='store_true',
                        help='批量发送所有类型测试数据后退出')
    parser.add_argument('--interactive', action='store_true',
                        help='进入交互模式（手动发送命令）')
    parser.add_argument('--interval', type=float, default=1.0,
                        help='批量发送间隔秒数 (默认: 1.0)')
    parser.add_argument('--loop', type=int, default=1,
                        help='批量发送循环次数 (默认: 1)')

    args = parser.parse_args()

    if not args.batch and not args.interactive:
        parser.print_help()
        print("\n请指定 --batch 或 --interactive 模式")
        return 1

    sim = GB26875Simulator(
        host=args.host,
        port=args.port,
        device_addr=args.device_addr,
        center_addr=args.center_addr,
        version=args.version,
        user_version=args.user_version
    )

    if not sim.connect():
        return 1

    sim.start_receiving()
    time.sleep(0.5)  # 等待接收线程就绪

    try:
        if args.batch:
            for i in range(args.loop):
                print(f"\n--- 第 {i+1}/{args.loop} 轮发送 ---")
                sim.send_batch_test()
                if i < args.loop - 1:
                    time.sleep(args.interval)
            sim.print_stats()

        elif args.interactive:
            print("\n=== 交互模式 ===")
            print("命令:")
            print("  1 - 发送系统状态")
            print("  2 - 发送部件运行状态")
            print("  3 - 发送模拟量(温度)")
            print("  4 - 发送操作信息")
            print("  5 - 发送软件版本")
            print("  6 - 发送传输装置状态")
            print("  a - 批量发送所有类型")
            print("  s - 显示统计")
            print("  q - 退出")
            print()

            while True:
                try:
                    cmd = input("> ").strip().lower()
                    if cmd == 'q' or cmd == 'quit':
                        break
                    elif cmd == '1':
                        sim.send_system_status()
                    elif cmd == '2':
                        sim.send_component_status()
                    elif cmd == '3':
                        sim.send_component_analog()
                    elif cmd == '4':
                        sim.send_operation_info()
                    elif cmd == '5':
                        sim.send_sw_version()
                    elif cmd == '6':
                        sim.send_tx_device_status()
                    elif cmd == 'a':
                        sim.send_batch_test()
                    elif cmd == 's':
                        sim.print_stats()
                    elif cmd == '':
                        continue
                    else:
                        print(f"未知命令: {cmd}")
                except KeyboardInterrupt:
                    break
                except EOFError:
                    break
            sim.print_stats()

    finally:
        sim.disconnect()

    return 0


if __name__ == '__main__':
    sys.exit(main())