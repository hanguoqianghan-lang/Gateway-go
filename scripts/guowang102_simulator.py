#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
国网102规约 模拟器 (子站/服务端模拟)
用于测试 guowang102 驱动的文件传输功能

运行方式:
    python scripts/guowang102_simulator.py --data-dir ./testdata/files --port 6960

功能:
- TCP Server 监听 6960 端口
- 实现链路层：固定帧/可变帧/0xE5、FCB 管理、ACD/DFC
- 实现文件传输：分帧发送 TypeID 139/144/147 文件
- 支持命令行参数：文件目录、发送间隔、异常注入
"""

import argparse
import asyncio
import logging
import os
import random
import struct
import time
from pathlib import Path
from typing import Dict, List, Optional, Tuple

# ─────────────────────────────────────────────────────────────────────────────
# 协议常量
# ─────────────────────────────────────────────────────────────────────────────

# 帧起始/结束
START_FIXED = 0x10
START_VARIABLE = 0x68
END_BYTE = 0x16
SINGLE_ACK = 0xE5

# 功能码 (FC)
FC_RESET_LINK = 0x00      # 复位链路
FC_START_TRANSFER = 0x04  # 启动数据传输
FC_REQUEST_LINK = 0x09    # 请求链路状态
FC_REQUEST_LEVEL1 = 0x0A  # 召唤1级用户数据
FC_REQUEST_LEVEL2 = 0x0B  # 召唤2级用户数据
FC_SEND_CONFIRM = 0x03    # 发送/确认数据 (FC=3)

# 上行功能码
FC_ACK = 0x00             # 确认
FC_LINK_BUSY = 0x01       # 链路忙
FC_DATA_RESPONSE = 0x08   # 以数据回答请求帧
FC_NO_DATA = 0x09         # 无所召唤数据
FC_STATUS_RESPONSE = 0x0B # 以链路状态/访问请求回答

# 传送原因 (COT) - 文件传输专用
COT_FILE_LAST_FRAME = 0x07       # 最后一帧
COT_FILE_NOT_LAST_FRAME = 0x08   # 非最后一帧
COT_FILE_RECV_COMPLETE = 0x0A    # 主站确认接收结束
COT_FILE_LEN_MATCH = 0x0B        # 子站确认长度一致
COT_FILE_LEN_MISMATCH = 0x0C     # 子站长度不一致
COT_FILE_DUPLICATE = 0x0D        # 主站检测到重复
COT_FILE_DUP_CONFIRMED = 0x0E    # 子站确认重复
COT_FILE_TOO_LONG = 0x0F         # 主站判定过长
COT_FILE_LONG_CONFIRMED = 0x10   # 子站确认过长
COT_FILE_NAME_INVALID = 0x11     # 主站判定文件名无效
COT_FILE_NAME_CONFIRMED = 0x12   # 子站确认文件名无效
COT_FRAME_TOO_LONG = 0x13        # 主站判定单帧过长
COT_FRAME_LONG_CONFIRMED = 0x14  # 子站确认单帧过长

# 类型标识
TYPE_ID_ENERGY_PRED = 139       # 0x8B 电量预测
TYPE_ID_SHORT_TERM = 144        # 0x90 短期预测
TYPE_ID_ULTRA_SHORT_TERM = 145  # 0x91 超短期预测
TYPE_ID_MAST_DATA = 146         # 0x92 测风/测光数据
TYPE_ID_UNIT_STATUS = 147       # 0x93 机组/逆变器状态

# 固定地址
LINK_ADDRESS = 0xFFFF
COMMON_ADDRESS = 0xFFFF
ORIGINATOR_ADDR = 0x00
RECORD_ADDRESS = 0x00

# 帧类型
FRAME_TYPE_FIXED = 0
FRAME_TYPE_VARIABLE = 1
FRAME_TYPE_SINGLE_ACK = 2

# ─────────────────────────────────────────────────────────────────────────────
# 工具函数
# ─────────────────────────────────────────────────────────────────────────────

def calc_cs(data: bytes) -> int:
    """计算校验和：字节累加和，保留低8位"""
    return sum(data) & 0xFF

def build_fixed_frame(control: int, address: int = LINK_ADDRESS) -> bytes:
    """构建固定帧: 10H | C | A_low | A_high | CS | 16H"""
    addr_low = address & 0xFF
    addr_high = (address >> 8) & 0xFF
    cs = calc_cs(bytes([control, addr_low, addr_high]))
    return bytes([START_FIXED, control, addr_low, addr_high, cs, END_BYTE])

def build_variable_frame(control: int, address: int, asdu: bytes) -> bytes:
    """构建可变帧: 68H | L | L | 68H | C | A_low | A_high | ASDU | CS | 16H"""
    l = len(asdu) + 3  # C(1) + A(2)
    if l > 255:
        l = 255
        asdu = asdu[:252]

    addr_low = address & 0xFF
    addr_high = (address >> 8) & 0xFF

    header = bytes([START_VARIABLE, l, l, START_VARIABLE, control, addr_low, addr_high])
    cs = calc_cs(header[4:] + asdu)
    return header + asdu + bytes([cs, END_BYTE])

def build_single_ack() -> bytes:
    """构建单字节确认"""
    return bytes([SINGLE_ACK])

def build_downlink_control(fcb: bool, fcv: bool, fc: int) -> int:
    """构建下行控制域"""
    ctrl = 0x40  # PRM=1 (Bit6)
    if fcb:
        ctrl |= 0x20  # FCB=1 (Bit5)
    if fcv:
        ctrl |= 0x10  # FCV=1 (Bit4)
    ctrl |= (fc & 0x0F)  # FC (Bit3-Bit0)
    return ctrl

def build_uplink_control(acd: bool, dfc: bool, fc: int) -> int:
    """构建上行控制域"""
    ctrl = 0
    if acd:
        ctrl |= 0x40  # ACD (Bit6)
    if dfc:
        ctrl |= 0x20  # DFC (Bit5)
    ctrl |= (fc & 0x0F)  # FC (Bit3-Bit0)
    return ctrl

def build_asdu(type_id: int, vsq: int, cot: int, origin_addr: int, common_addr: int, record_addr: int, payload: bytes) -> bytes:
    """构建 ASDU"""
    ca_low = common_addr & 0xFF
    ca_high = (common_addr >> 8) & 0xFF

    header = bytes([
        type_id, vsq, cot, origin_addr,
        ca_low, ca_high, record_addr
    ])
    return header + payload

def build_file_transfer_asdu(type_id: int, cot: int, file_name: str, file_content: bytes) -> bytes:
    """构建文件传输 ASDU"""
    # 文件名 32 字节固定长度，左对齐右补零
    name_bytes = file_name.encode('ascii', errors='ignore')[:32]
    name_padded = name_bytes.ljust(32, b'\x00')

    payload = name_padded + file_content

    return build_asdu(type_id, 0x01, cot, ORIGINATOR_ADDR, COMMON_ADDRESS, RECORD_ADDRESS, payload)

def parse_frame(data: bytes) -> Optional[dict]:
    """解析帧"""
    if not data:
        return None

    if data[0] == START_FIXED:
        return parse_fixed_frame(data)
    elif data[0] == START_VARIABLE:
        return parse_variable_frame(data)
    elif data[0] == SINGLE_ACK:
        return {'type': FRAME_TYPE_SINGLE_ACK, 'raw': data}
    return None

def parse_fixed_frame(data: bytes) -> Optional[dict]:
    """解析固定帧"""
    if len(data) < 6:
        return None
    if data[5] != END_BYTE:
        return None

    control = data[1]
    address = data[2] | (data[3] << 8)
    cs = data[4]
    expected_cs = calc_cs(data[1:4])

    return {
        'type': FRAME_TYPE_FIXED,
        'control': control,
        'address': address,
        'cs': cs,
        'cs_valid': cs == expected_cs,
        'fc': control & 0x0F,
        'prm': (control & 0x40) != 0,
        'fcb': (control & 0x20) != 0,
        'fcv': (control & 0x10) != 0,
        'raw': data
    }

def parse_variable_frame(data: bytes) -> Optional[dict]:
    """解析可变帧"""
    if len(data) < 9:
        return None

    l1 = data[1]
    l2 = data[2]
    if l1 != l2:
        return None

    if data[3] != START_VARIABLE:
        return None

    expected_len = l1 + 6
    if len(data) < expected_len:
        return None

    if data[expected_len - 1] != END_BYTE:
        return None

    control = data[4]
    address = data[5] | (data[6] << 8)
    asdu_len = l1 - 3
    asdu = data[7:7 + asdu_len] if asdu_len > 0 else b''
    cs = data[expected_len - 2]
    cs_data = data[4:expected_len - 2]
    expected_cs = calc_cs(cs_data)

    return {
        'type': FRAME_TYPE_VARIABLE,
        'control': control,
        'address': address,
        'asdu': asdu,
        'cs': cs,
        'cs_valid': cs == expected_cs,
        'acd': (control & 0x40) != 0,
        'dfc': (control & 0x20) != 0,
        'fc': control & 0x0F,
        'raw': data[:expected_len]
    }

# ─────────────────────────────────────────────────────────────────────────────
# 模拟器核心类
# ─────────────────────────────────────────────────────────────────────────────

class ClientSession:
    """客户端会话状态"""
    def __init__(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter, client_id: int):
        self.reader = reader
        self.writer = writer
        self.client_id = client_id
        self.peer = writer.get_extra_info('peername')

        # 链路层状态
        self.state = 'DISCONNECTED'  # DISCONNECTED, RESET_SENT, RESET_CONFIRMED, TRANSFER_STARTED, OPERATIONAL
        self.send_fcb = False
        self.recv_fcb = False
        self.pending_fcb = False
        self.retry_count = 0
        self.max_retries = 3

        # 文件传输队列
        self.file_queue: List[Dict] = []
        self.current_file: Optional[Dict] = None
        self.file_chunk_index = 0
        self.file_chunks: List[bytes] = []

        # 统计
        self.stats = {
            'frames_rx': 0,
            'frames_tx': 0,
            'files_sent': 0,
            'bytes_sent': 0,
            'errors': 0,
            'retries': 0,
            'acd_sent': 0,
        }

class GuoWang102Simulator:
    """国网102模拟器主类"""

    def __init__(self, data_dir: str, port: int = 6960, send_interval: float = 1.0,
                 max_chunk_size: int = 200, enable_acd: bool = True,
                 inject_errors: bool = False, log_level: str = "INFO"):
        self.data_dir = Path(data_dir)
        self.port = port
        self.send_interval = send_interval
        self.max_chunk_size = max_chunk_size
        self.enable_acd = enable_acd
        self.inject_errors = inject_errors
        self.log_level = log_level

        # 服务器
        self.server: Optional[asyncio.Server] = None
        self.sessions: Dict[int, ClientSession] = {}
        self.session_counter = 0
        self.running = False

        # 日志
        self.logger = logging.getLogger("guowang102_simulator")
        self.logger.setLevel(getattr(logging, log_level))
        handler = logging.StreamHandler()
        handler.setFormatter(logging.Formatter(
            '%(asctime)s %(levelname)s [%(name)s] %(message)s',
            datefmt='%H:%M:%S'
        ))
        self.logger.addHandler(handler)

        # 预加载文件
        self.file_cache: Dict[str, bytes] = {}
        self._load_files()

    def _load_files(self):
        """预加载数据目录下的文件"""
        if not self.data_dir.exists():
            self.logger.warning(f"Data directory not found: {self.data_dir}")
            return

        for ext in ['.wpd', '.WPD', '.txt', '.dat', '.bin']:
            for f in self.data_dir.glob(f'*{ext}'):
                try:
                    with open(f, 'rb') as fp:
                        content = fp.read()
                    self.file_cache[f.name] = content
                    self.logger.info(f"Loaded file: {f.name} ({len(content)} bytes)")
                except Exception as e:
                    self.logger.error(f"Failed to load {f}: {e}")

        if not self.file_cache:
            # 创建一些测试文件
            self._create_test_files()

    def _create_test_files(self):
        """创建测试用的模拟文件"""
        test_files = {
            'WF_20240730_001.wpd': f'EnergyPrediction,{time.strftime("%Y%m%d%H%M%S")},001,25.5,30.2,28.1\n' * 100,
            'ST_20240730_001.wpd': f'ShortTerm,{time.strftime("%Y%m%d%H%M%S")},001,24.8,29.5,27.3\n' * 80,
            'US_20240730_001.wpd': f'UltraShortTerm,{time.strftime("%Y%m%d%H%M%S")},001,25.1,28.9,26.7\n' * 120,
            'MT_20240730_001.wpd': f'MastData,{time.strftime("%Y%m%d%H%M%S")},T001,12.5,25.3,45\n' * 60,
            'UN_20240730_001.wpd': f'UnitStatus,{time.strftime("%Y%m%d%H%M%S")},U001,1,1500,0.95\n' * 50,
        }

        self.data_dir.mkdir(parents=True, exist_ok=True)
        for name, content in test_files.items():
            path = self.data_dir / name
            if not path.exists():
                with open(path, 'wb') as f:
                    f.write(content.encode('ascii'))
                self.file_cache[name] = content.encode('ascii')
                self.logger.info(f"Created test file: {name} ({len(content)} bytes)")

    def _get_file_type_id(self, file_name: str) -> int:
        """根据文件名前缀确定 TypeID"""
        name_upper = file_name.upper()
        if name_upper.startswith('WF_'):
            return TYPE_ID_ENERGY_PRED
        elif name_upper.startswith('ST_'):
            return TYPE_ID_SHORT_TERM
        elif name_upper.startswith('US_'):
            return TYPE_ID_ULTRA_SHORT_TERM
        elif name_upper.startswith('MT_'):
            return TYPE_ID_MAST_DATA
        elif name_upper.startswith('UN_'):
            return TYPE_ID_UNIT_STATUS
        return TYPE_ID_ENERGY_PRED  # 默认

    def _chunk_file(self, content: bytes) -> List[bytes]:
        """将文件内容分块"""
        chunks = []
        for i in range(0, len(content), self.max_chunk_size):
            chunks.append(content[i:i + self.max_chunk_size])
        return chunks

    async def start(self):
        """启动模拟器"""
        self.running = True
        self.server = await asyncio.start_server(
            self._handle_client, '0.0.0.0', self.port
        )

        addr = self.server.sockets[0].getsockname()
        self.logger.info(f"GuoWang102 Simulator listening on {addr[0]}:{addr[1]}")
        self.logger.info(f"Data directory: {self.data_dir}")
        self.logger.info(f"Loaded {len(self.file_cache)} files")

        async with self.server:
            await self.server.serve_forever()

    async def _handle_client(self, reader: asyncio.StreamReader, writer: asyncio.StreamWriter):
        """处理客户端连接"""
        self.session_counter += 1
        session = ClientSession(reader, writer, self.session_counter)
        self.sessions[session.client_id] = session

        self.logger.info(f"Client #{session.client_id} connected from {session.peer}")

        try:
            # 发送缓冲区
            send_buffer = bytearray()

            while self.running:
                # 尝试读取数据
                try:
                    data = await asyncio.wait_for(reader.read(4096), timeout=1.0)
                except asyncio.TimeoutError:
                    # 定时处理：检查发送队列、发送心跳等
                    await self._process_session_timers(session)
                    continue

                if not data:
                    self.logger.info(f"Client #{session.client_id} disconnected")
                    break

                send_buffer.extend(data)

                # 处理缓冲区中的完整帧
                while True:
                    frame, consumed = self._extract_frame(send_buffer)
                    if frame is None:
                        break

                    send_buffer = send_buffer[consumed:]
                    await self._handle_frame(session, frame)

        except asyncio.CancelledError:
            pass
        except Exception as e:
            self.logger.error(f"Client #{session.client_id} error: {e}")
        finally:
            session.writer.close()
            await session.writer.wait_closed()
            self.sessions.pop(session.client_id, None)
            self.logger.info(f"Client #{session.client_id} session ended")

    def _extract_frame(self, buffer: bytearray) -> Tuple[Optional[bytes], int]:
        """从缓冲区提取完整帧"""
        if not buffer:
            return None, 0

        # 单字节确认
        if buffer[0] == SINGLE_ACK:
            return bytes([SINGLE_ACK]), 1

        # 固定帧: 10H ... 16H (固定6字节)
        if buffer[0] == START_FIXED:
            if len(buffer) >= 6:
                if buffer[5] == END_BYTE:
                    return bytes(buffer[:6]), 6
            return None, 0

        # 可变帧: 68H L L 68H ... 16H
        if buffer[0] == START_VARIABLE:
            if len(buffer) < 4:
                return None, 0

            l = buffer[1]
            if buffer[2] != l:
                # 长度不匹配，跳过这个字节尝试重新同步
                return None, 1

            if buffer[3] != START_VARIABLE:
                return None, 1

            expected_len = l + 6
            if len(buffer) >= expected_len:
                if buffer[expected_len - 1] == END_BYTE:
                    return bytes(buffer[:expected_len]), expected_len
            return None, 0

        # 未知起始字节，跳过
        return None, 1

    async def _handle_frame(self, session: ClientSession, frame_data: bytes):
        """处理接收到的帧"""
        session.stats['frames_rx'] += 1
        frame = parse_frame(frame_data)

        if frame is None:
            self.logger.warning(f"Client #{session.client_id}: Failed to parse frame: {frame_data.hex()}")
            session.stats['errors'] += 1
            return

        self.logger.debug(f"Client #{session.client_id} RX: {frame}")

        if frame['type'] == FRAME_TYPE_SINGLE_ACK:
            await self._handle_single_ack(session, frame)

        elif frame['type'] == FRAME_TYPE_FIXED:
            await self._handle_fixed_frame(session, frame)

        elif frame['type'] == FRAME_TYPE_VARIABLE:
            await self._handle_variable_frame(session, frame)

    async def _handle_single_ack(self, session: ClientSession, frame: dict):
        """处理单字节确认 (0xE5)"""
        self.logger.debug(f"Client #{session.client_id}: Received 0xE5 ACK")

        if session.pending_fcb:
            session.pending_fcb = False
            session.retry_count = 0
            self.logger.debug(f"Client #{session.client_id}: FCB confirmed")

    async def _handle_fixed_frame(self, session: ClientSession, frame: dict):
        """处理固定帧"""
        fc = frame['fc']
        fcb = frame['fcb']

        if fc == FC_RESET_LINK:
            # 复位链路命令
            self.logger.info(f"Client #{session.client_id}: Reset link received (FCB={fcb})")
            session.state = 'RESET_CONFIRMED'
            session.send_fcb = False
            session.recv_fcb = False

            # 回复确认 (0xE5 或 固定帧 FC=0)
            ack = build_single_ack()
            await self._send_raw(session, ack)

        elif fc == FC_START_TRANSFER:
            # 启动数据传输
            self.logger.info(f"Client #{session.client_id}: Start data transfer received")
            session.state = 'TRANSFER_STARTED'

            # 回复确认
            ack = build_single_ack()
            await self._send_raw(session, ack)

        elif fc == FC_REQUEST_LINK:
            # 链路状态请求 (FC=9)
            self.logger.debug(f"Client #{session.client_id}: Link status request")
            if session.state == 'TRANSFER_STARTED':
                session.state = 'OPERATIONAL'
                self.logger.info(f"Client #{session.client_id}: Link operational")

            # 回复链路状态 (FC=11 上行)
            ctrl = build_uplink_control(False, False, FC_STATUS_RESPONSE)
            resp = build_fixed_frame(ctrl)
            await self._send_raw(session, resp)

        elif fc == FC_REQUEST_LEVEL1:
            # 召唤1级用户数据 (FC=10)
            self.logger.info(f"Client #{session.client_id}: Request level 1 data (FC=10)")
            await self._handle_request_level1(session)

        elif fc == FC_REQUEST_LEVEL2:
            # 召唤2级用户数据 (FC=11)
            self.logger.debug(f"Client #{session.client_id}: Request level 2 data (FC=11)")
            await self._handle_request_level2(session)

        elif fc == FC_SEND_CONFIRM:
            # 主站发送确认数据 (FC=3)，通常是应答
            self.logger.debug(f"Client #{session.client_id}: FC=3 received")

    async def _handle_variable_frame(self, session: ClientSession, frame: dict):
        """处理可变帧 (携带 ASDU)"""
        asdu = frame['asdu']
        if len(asdu) < 7:
            return

        type_id = asdu[0]
        vsq = asdu[1]
        cot = asdu[2]

        self.logger.debug(f"Client #{session.client_id}: ASDU TypeID={type_id}, COT={cot:#02x}")

        # 处理文件接收完成确认 (COT=0x0A)
        if cot == COT_FILE_RECV_COMPLETE:
            await self._handle_file_recv_complete(session, frame)

    async def _handle_request_level1(self, session: ClientSession):
        """处理召唤1级数据请求 - 发送文件数据"""
        if session.state != 'OPERATIONAL':
            self.logger.warning(f"Client #{session.client_id}: Not operational, cannot send files")
            return

        # 如果有待发送文件，发送下一个分片
        if session.current_file:
            await self._send_next_file_chunk(session)
        elif session.file_queue:
            # 开始新文件
            session.current_file = session.file_queue.pop(0)
            session.file_chunks = self._chunk_file(session.current_file['content'])
            session.file_chunk_index = 0

            file_name = session.current_file['name']
            type_id = session.current_file['type_id']
            total_chunks = len(session.file_chunks)

            self.logger.info(f"Client #{session.client_id}: Starting file transfer: {file_name} "
                           f"(TypeID={type_id}, {total_chunks} chunks, {session.current_file['size']} bytes)")

            await self._send_next_file_chunk(session)
        else:
            # 无数据，回复无数据帧
            self.logger.debug(f"Client #{session.client_id}: No files to send")
            ctrl = build_uplink_control(False, False, FC_NO_DATA)
            resp = build_fixed_frame(ctrl)
            await self._send_raw(session, resp)

    async def _handle_request_level2(self, session: ClientSession):
        """处理召唤2级数据请求 - 放入文件队列"""
        if session.state != 'OPERATIONAL':
            return

        # 将所有文件加入队列
        for file_name, content in self.file_cache.items():
            file_info = {
                'name': file_name,
                'content': content,
                'size': len(content),
                'type_id': self._get_file_type_id(file_name),
            }
            session.file_queue.append(file_info)

        self.logger.info(f"Client #{session.client_id}: Queued {len(session.file_queue)} files for transfer")

        # 回复确认 (FC=8 有数据回答)
        ctrl = build_uplink_control(True, False, FC_DATA_RESPONSE)  # ACD=1 表示有1级数据
        resp = build_fixed_frame(ctrl)
        await self._send_raw(session, resp)

        session.stats['acd_sent'] += 1

    async def _send_next_file_chunk(self, session: ClientSession):
        """发送下一个文件分片"""
        if not session.current_file or session.file_chunk_index >= len(session.file_chunks):
            # 文件发送完成
            await self._finish_file_transfer(session)
            return

        chunk = session.file_chunks[session.file_chunk_index]
        is_last = (session.file_chunk_index == len(session.file_chunks) - 1)
        file_name = session.current_file['name']
        type_id = session.current_file['type_id']

        # COT: 最后一帧 0x07，非最后一帧 0x08
        cot = COT_FILE_LAST_FRAME if is_last else COT_FILE_NOT_LAST_FRAME

        asdu = build_file_transfer_asdu(type_id, cot, file_name, chunk)
        ctrl = build_downlink_control(session.send_fcb, True, FC_SEND_CONFIRM)
        frame = build_variable_frame(ctrl, LINK_ADDRESS, asdu)

        # 翻转 FCB
        session.send_fcb = not session.send_fcb
        session.pending_fcb = session.send_fcb
        session.retry_count = 0

        await self._send_raw(session, frame)

        session.stats['frames_tx'] += 1
        session.stats['bytes_sent'] += len(chunk)

        self.logger.debug(f"Client #{session.client_id}: Sent chunk {session.file_chunk_index + 1}/{len(session.file_chunks)} "
                         f"({len(chunk)} bytes, last={is_last}) for {file_name}")

        session.file_chunk_index += 1

        # 模拟发送间隔
        await asyncio.sleep(self.send_interval)

    async def _finish_file_transfer(self, session: ClientSession):
        """文件传输完成，等待主站 COT=0x0A 确认"""
        file_name = session.current_file['name']
        self.logger.info(f"Client #{session.client_id}: File transfer completed: {file_name}")
        session.stats['files_sent'] += 1
        session.current_file = None
        session.file_chunks = []
        session.file_chunk_index = 0

    async def _handle_file_recv_complete(self, session: ClientSession, frame: dict):
        """处理主站文件接收完成确认 (COT=0x0A)"""
        self.logger.info(f"Client #{session.client_id}: Received file recv complete (COT=0x0A)")

        # 回复长度匹配确认 (COT=0x0B)
        asdu = frame['asdu']
        if len(asdu) >= 7:
            type_id = asdu[0]
            cot = COT_FILE_LEN_MATCH
            resp_asdu = build_asdu(type_id, 0x01, cot, ORIGINATOR_ADDR, COMMON_ADDRESS, RECORD_ADDRESS, b'')
            ctrl = build_downlink_control(session.send_fcb, True, FC_SEND_CONFIRM)
            resp_frame = build_variable_frame(ctrl, LINK_ADDRESS, resp_asdu)

            session.send_fcb = not session.send_fcb
            session.pending_fcb = session.send_fcb

            await self._send_raw(session, resp_frame)
            self.logger.debug(f"Client #{session.client_id}: Sent COT=0x0B (len match)")

    async def _process_session_timers(self, session: ClientSession):
        """定时处理：重传、心跳等"""
        # 重传逻辑
        if session.pending_fcb and session.retry_count < session.max_retries:
            session.retry_count += 1
            session.stats['retries'] += 1
            self.logger.warning(f"Client #{session.client_id}: Retry #{session.retry_count}")

        # 定期检查是否需要发送文件 (ACD 机制)
        if session.state == 'OPERATIONAL' and session.file_queue and self.enable_acd:
            # 已经在 FC=11 响应中设置了 ACD
            pass

    async def _send_raw(self, session: ClientSession, data: bytes):
        """发送原始数据"""
        try:
            session.writer.write(data)
            await session.writer.drain()
            session.stats['frames_tx'] += 1
        except Exception as e:
            self.logger.error(f"Client #{session.client_id}: Send failed: {e}")
            session.stats['errors'] += 1

    def print_stats(self):
        """打印统计信息"""
        print("\n" + "=" * 60)
        print("GuoWang102 Simulator Statistics")
        print("=" * 60)
        print(f"Active sessions: {len(self.sessions)}")
        print(f"Files in cache: {len(self.file_cache)}")
        for sid, session in self.sessions.items():
            print(f"\nClient #{sid} ({session.peer}):")
            print(f"  State: {session.state}")
            print(f"  Frames RX: {session.stats['frames_rx']}")
            print(f"  Frames TX: {session.stats['frames_tx']}")
            print(f"  Files sent: {session.stats['files_sent']}")
            print(f"  Bytes sent: {session.stats['bytes_sent']}")
            print(f"  Errors: {session.stats['errors']}")
            print(f"  Retries: {session.stats['retries']}")
            print(f"  ACD sent: {session.stats['acd_sent']}")
        print("=" * 60)


# ─────────────────────────────────────────────────────────────────────────────
# 入口
# ─────────────────────────────────────────────────────────────────────────────

async def main():
    parser = argparse.ArgumentParser(description='GuoWang102 Simulator (Substation)')
    parser.add_argument('--data-dir', default='./testdata/files', help='Data directory')
    parser.add_argument('--port', type=int, default=6960, help='TCP port (default 6960)')
    parser.add_argument('--interval', type=float, default=0.5, help='Send interval seconds (default 0.5)')
    parser.add_argument('--chunk-size', type=int, default=200, help='Max chunk size (default 200)')
    parser.add_argument('--no-acd', action='store_true', help='Disable ACD notification')
    parser.add_argument('--inject-errors', action='store_true', help='Inject random errors')
    parser.add_argument('-v', '--verbose', action='store_true', help='Debug logging')

    args = parser.parse_args()

    log_level = "DEBUG" if args.verbose else "INFO"

    simulator = GuoWang102Simulator(
        data_dir=args.data_dir,
        port=args.port,
        send_interval=args.interval,
        max_chunk_size=args.chunk_size,
        enable_acd=not args.no_acd,
        inject_errors=args.inject_errors,
        log_level=log_level
    )

    # 信号处理
    loop = asyncio.get_running_loop()
    for sig_name in ('SIGINT', 'SIGTERM'):
        try:
            sig = getattr(signal, sig_name)
            loop.add_signal_handler(sig, lambda: asyncio.create_task(shutdown(simulator)))
        except (AttributeError, NotImplementedError):
            pass

    try:
        await simulator.start()
    except KeyboardInterrupt:
        pass
    finally:
        simulator.print_stats()


async def shutdown(simulator: GuoWang102Simulator):
    simulator.logger.info("Shutting down...")
    simulator.running = False
    if simulator.server:
        simulator.server.close()
        await simulator.server.wait_closed()


if __name__ == '__main__':
    import signal
    asyncio.run(main())