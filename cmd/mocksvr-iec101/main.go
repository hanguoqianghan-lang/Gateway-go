// cmd/mocksvr-iec101/main.go - IEC 60870-5-101 子站 (RTU) 模拟器 (mock)
//
// 目的：在调试 IEC101 南向驱动时提供一个可控、轻量的子站模拟器。
//
// 本程序扮演你提交的"电力现场 IEC101 协议"文档中的子站（从动站），
// 主站（IEC101 驱动 / gateway）连接它后能走完完整链路：
//   - 询问链路状态          → 回复 0x10 0x0B 0x01 0x0C 0x16
//   - 复位链路              → 回复 0x10 0x00 0x01 0x01 0x16（ACD=0，不召唤一级数据）
//   - 总召唤 (general interrogation) → 回复：
//         0x68 0x09 0x09 0x68 0x00 0x01 0x64 0x01 0x07 0x01 0x00 0x00 0x14 ...
//         (遥信：成组单点 0x14, 96 个遥信点 1~96)
//         (遥测：测量值 0x09, 96 个遥测点 0x95 起的 96 个 float)
//         (总召唤结束 0x64 0x0a)
//
// 传输方式：TCP（不走真实串口，调试期间便于临时打断 / 注释）。
//  - host: 127.0.0.1
//  - port: 默认 8881（用 -port 可改）
//
// 注意：本模拟器只实现你文档中明确给出的子站→主站方向报文，
// 不实现双点遥控 / SOE / 时钟同步 / 召唤 1 级数据；为最小代价跑通
// "主站连得上 + 总召唤能拿到数据" 闭环。如需其它方向以后按需加。
//
// 用法：
//   go run ./cmd/mocksvr-iec101/ -port 8881
//   （同时启动 gateway 配置，IEC101 driver 走 TCP-to-serial 适配器连该端口）
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// 帧常量
const (
	startFixed    byte = 0x10 // 固定帧启动字符
	startVariable byte = 0x68 // 可变帧启动字符
	endByte       byte = 0x16 // 结束字符

	// 链路地址（假设为 1）
	linkAddr byte = 0x01

	// 类型标识 (按你给的文档)
	typeGI          byte = 0x64 // 总召唤
	typeSingleGroup byte = 0x14 // 成组单点遥信（不带时标，每个字节位 8 个遥信）
	typeMeasNormal  byte = 0x09 // 带品质描述的归一化测量值，每个值 3 字节
)

// helper: 计算 CS = sum(data) 取低 8 位
func calcCS(data []byte) byte {
	var sum byte
	for _, b := range data {
		sum += b
	}
	return sum
}

// buildFixedFrame 生成固定长度帧 (10 | C | A | CS | 16)
func buildFixedFrame(control byte) []byte {
	return []byte{startFixed, control, linkAddr, calcCS([]byte{control, linkAddr}), endByte}
}

// buildVariableFrame 生成可变长度帧 (68 | L | L | 68 | C | A | ASDU | CS | 16)
//
// IEC 60870-5-101 规定可变帧长 L ∈ [3, 255]：
//   - L = ASDU 字节数 + 控制域 1 + 地址域 1
//   - L 大于 0xFF 必须「分帧」：把 ASDU 拆成若干段，每段单独组成可变帧 + 单独的
//     COT=后续序号，但本工具目前只发出单帧；分帧逻辑（基于总召唤 GI）属于上层调用者。
//
// 本函数保证正确计算 L（不截断、不溢出）。调用方如果 ASDU 太长超出 255，需要
// 在外层手动拆分并多次调用本函数，每次配对应 COT（VSQ 字段）。
//
// 旧版本曾出现以下 bug：
//   - `if userLen > 0xFF { userLen = 0xFF }` —— 这把 userLen 改了，但
//     真正写入缓冲区的还是完整 asdu，导致 L 说不匹配状态。
//   - CS 计算：`calcCS(buf[4 : 4+3+len(asdu)-2])` 取错起始终止，asdu=2 时 panic。
// 这里重写为：先算出 userLen，如果 > 0xFF 返回 nil 提示调用方拆分；
// CS 用清晰的起止区间 [C | A | | asdu]。
func buildVariableFrame(control byte, asdu []byte) []byte {
	userLen := 2 + len(asdu) // C(1) + A(1) + ASDU
	if userLen > 0xFF {
		// 超长交由调用方拆分。本函数拒绝截断。
		return nil
	}
	if userLen < 3 {
		// IEC 60870-5 规定最小 L=3
		return nil
	}

	buf := make([]byte, 0, 4+userLen+2)
	buf = append(buf, startVariable)
	buf = append(buf, byte(userLen))
	buf = append(buf, byte(userLen))
	buf = append(buf, startVariable)

	buf = append(buf, control)
	buf = append(buf, linkAddr)
	buf = append(buf, asdu...)

	// CS 覆盖区间: 从 control 开始, 长度 = 2 + len(asdu)
	csStart := 4                                  // 即控制域所在的偏移
	csLen := 2 + len(asdu)                        // C + A + ASDU
	cs := calcCS(buf[csStart : csStart+csLen])
	buf = append(buf, cs)
	buf = append(buf, endByte)
	return buf
}

// buildGIConfirm 构造"总召唤确认"报文，CA 与主站呼叫保持一致。
//
// 格式：
//   68 09 09 68 | 00 01 | 64 01 07 01 | 00 00 | 14 | xx 16
//
// 说明：type=0x64（召唤, 全数据），COT=0x07（激活确认），公共地址=01，
// 信息体地址 0x0000，QOI=0x14。
func buildGIConfirm(ca byte) []byte {
	asdu := []byte{
		typeGI,        // type = 0x64
		0x01,          // VSQ（单个信息体）
		0x07,          // COT = 激活确认
		ca,            // 公共地址
		0x00, 0x00,    // 信息体地址 (2 字节 LE)
		0x14,          // QOI
	}
	return buildVariableFrame(0x00, asdu)
}

// buildGIEnd 构造"总召唤结束"报文，COT=0x0a（激活结束）。
func buildGIEnd(ca byte) []byte {
	asdu := []byte{
		typeGI,
		0x01,
		0x0a, // COT = 激活结束
		ca,
		0x00, 0x00,
		0x14,
	}
	return buildVariableFrame(0x00, asdu)
}

// buildYCValueGroup 构造"成组单点遥信"，含 N=eachBytePerBit (64 个遥信，
// 每字节 8 个遥信 = 8 字节)，从 ioaStart 起 ioa 顺序递增。
//
// 文档示例（你给的现实报文）：
//   68 29 29 68 28 01 14 08 14 01 01 00 80 04 00 00 00 11 00 00 00 00 00 21 00 00 00 00 00 ...
//
// 注意：
//   - 信息体里每"组"是 16 个遥信 = 2 字节 + 2 字节（state-bits + change-bits）
//   - 这里为最简化，做成单组（VSQ=08，即 8 个信息体 / 8 组），
//     但你文档示例 VSQ=08 的语义是 8 个 16-bit 信息体汇总 128 个遥信。
//   - 为保 mock 与文档"看起来对得上"，采用 8 组 x 8遥信 = 64 个遥信。
func buildYCValueGroup(ca byte, ioaStart uint16) []byte {
	// 单组内容：起始 IOA(2 字节) + 8 比特的遥信值 = 1 字节 + 1 字节 状态变位检出 = 2 字节 + 1 字节 品质描述 = 5 字节 / 组
	// 在你给的真实报文里：01 00 80 04 00 00 00，是 起始 IOA(2) + "16个遥信 (2)" + "检出 (2)" + 描述(1)
	// 但成组单点一般 16 个/组 (VSQ=8)
	// 我们就做"VSQ=8，每组 16 遥信 = 2 字节" => 8 组 * (3 字节 信息体 + 品质) = ~32 字节
	// 信息体内容:  起始 IOA (2) + state byte (2) + change byte (2) + qds (1)

	const groupCount = 8 // VSQ = 8
	infoObj := make([]byte, 0, groupCount*5)
	for i := 0; i < groupCount; i++ {
		startIOA := uint16(ioaStart + uint16(i*16))
		ioaBytes := []byte{byte(startIOA & 0xFF), byte((startIOA >> 8) & 0xFF)}
		// 16 遥信值 交替 0x80 / 0x00，方便看到这是调度
		state := byte(0x80)
		if i%2 == 1 {
			state = 0x00
		}
		infoObj = append(infoObj, ioaBytes...) // 起始 IOA
		infoObj = append(infoObj, state, 0x00) // 2 字节 state
		infoObj = append(infoObj, 0x00, 0x00)  // 2 字节 change-bits
	}

	// ASDU
	asdu := []byte{
		typeSingleGroup,    // 0x14
		byte(groupCount),   // VSQ
		0x14,              // COT = 响应总召唤 (=20=0x14)
		ca,                // CA
	}
	asdu = append(asdu, infoObj...)
	return buildVariableFrame(0x00, asdu)
}

// buildTelemetryFrames 构造"带品质的测量值归一化值"，VSQ=N，
// 每组分 3 字节 (2 字节 value + 1 字节 QDS)。起始 IOA 从 ioaStart 起。
//
// 文档: type = 0x09, VSQ = 96.
//
// 实现：单帧 ASDU 长度受 L≤253 限制（每点 5 字节 + 5 头，最多放下 49 个点）。
// 多于此数量的总召唤 → 自动拆分多个子帧，依次写在 c 上。
//
//   - first-frame 子帧 COT 仍 = 0x14（响应总召唤）；
//   - 子帧意义里 IEC60870-5 规定靠 VSQ bit 7 (sq) + IOA 推算，但你给的真实现场
//     没示范编号逻辑，这里采用一种简单的连续子帧推进：每子帧仅在第一帧带 IOA 起始值，
//     后续子帧 IOA 隐含「上一个 IOA + previous count」。
//     调度解不认这个可焚。为遵从你文档现场逻辑，后续调整为 **以单个 IOA 起始第一个
//     子帧 + 拆成 seq-numbered 多帧**，这里默认拆成「首个以外的同号」
//     (vsq 仍然写清本帧 info obj count，让入门驱动能读)。
func buildTelemetryFrames(c net.Conn, ca byte, ioaStart uint16, total int) {
	// 单帧最多 49 个点（49 * 5 = 245 + 5 头 = 250, 仍 ≤ 253）
	const perFrame = 49
	frameIdx := 0
	for offset := 0; offset < total; offset += perFrame {
		count := perFrame
		if offset+count > total {
			count = total - offset
		}
		infoObj := make([]byte, 0, 5*count)
		for i := 0; i < count; i++ {
			ioa := uint16(ioaStart) + uint16(offset+i)
			ioaBytes := []byte{byte(ioa & 0xFF), byte((ioa >> 8) & 0xFF)}
			val := uint16(((offset + i) * 100) & 0xFFFF) // 模拟数值（递增加）
			valBytes := []byte{byte(val & 0xFF), byte((val >> 8) & 0xFF)}
			qds := byte(0x00) // 良好品质
			infoObj = append(infoObj, ioaBytes...)
			infoObj = append(infoObj, valBytes...)
			infoObj = append(infoObj, qds)
		}
		asdu := []byte{
			typeMeasNormal,
			byte(count),
			0x14, // COT = 响应总召唤
			ca,
		}
		asdu = append(asdu, infoObj...)

		frame := buildVariableFrame(0x00, asdu)
		if frame == nil {
			log.Printf("[mocksvr-iec101] FATAL: telemetry subframe %d too large after split (asdu=%d)", frameIdx, len(asdu))
			return
		}
		c.Write(frame)
		log.Printf("[mocksvr-iec101] >>> telemetry 0x09 subframe %d (count=%d, total=%d bytes)", frameIdx, count, len(frame))
		frameIdx++
	}
}

// receiveFixedFrame 简化接收固定帧 5 字节。
func receiveFixedFrame(c net.Conn) (control byte, err error) {
	buf := make([]byte, 5)
	_, err = io.ReadFull(c, buf)
	if err != nil {
		return
	}
	if buf[0] != startFixed || buf[4] != endByte {
		return 0, fmt.Errorf("bad fixed frame: % x", buf)
	}
	if calcCS([]byte{buf[1], buf[2]}) != buf[3] {
		return 0, fmt.Errorf("CS mismatch in fixed frame")
	}
	return buf[1], nil
}

// receiveVariableFrame 简化接收可变帧。
func receiveVariableFrame(c net.Conn) (control byte, asdu []byte, err error) {
	head := make([]byte, 4)
	if _, err = io.ReadFull(c, head); err != nil {
		return
	}
	if head[0] != startVariable || head[1] != head[2] || head[3] != startVariable {
		return 0, nil, fmt.Errorf("bad variable frame header: % x", head)
	}
	bodyLen := int(head[1])
	body := make([]byte, bodyLen+2) // +CS +END
	if _, err = io.ReadFull(c, body); err != nil {
		return
	}
	if body[bodyLen+1] != endByte {
		return 0, nil, fmt.Errorf("bad variable frame ending byte: % x", body)
	}
	if calcCS(body[:bodyLen]) != body[bodyLen] {
		return 0, nil, fmt.Errorf("CS mismatch in variable frame")
	}
	control = body[0]
	asdu = body[2:bodyLen]
	return
}

// serve 监听一个连接 + 处理文档上的几种报文。
func serve(c net.Conn) {
	defer c.Close()
	log.Printf("[mocksvr-iec101] connected from %s", c.RemoteAddr())

	for {
		// peek 第一字节
		first := make([]byte, 1)
		_, err := c.Read(first)
		if err != nil {
			log.Printf("[mocksvr-iec101] read first byte failed: %v", err)
			return
		}
		c.SetReadDeadline(time.Now().Add(10 * time.Second))

		switch first[0] {
		case startFixed:
			// 重组 + 解析
			buf := append([]byte(nil), first...)
			rest := make([]byte, 4)
			if _, err := io.ReadFull(c, rest); err != nil {
				return
			}
			buf = append(buf, rest...)

			if buf[0] != startFixed || buf[4] != endByte {
				log.Printf("[mocksvr-iec101] bad fixed frame % x", buf)
				return
			}
			if calcCS([]byte{buf[1], buf[2]}) != buf[3] {
				log.Printf("[mocksvr-iec101] CS mismatch % x", buf)
				return
			}

			control := buf[1]
			log.Printf("[mocksvr-iec101] <<< fixed C=0x%02X A=0x%02X", control, buf[2])

			// 根据控制域 FC (低 5 位) 分发
			fc := control & 0x1F
			switch fc {
			case 0x09: // 请求链路状态
				// 子站回应：0x10 0x0B 0x01 0x0C 0x16
				resp := buildFixedFrame(0x0B)
				c.Write(resp)
				log.Printf("[mocksvr-iec101] >>> link status OK % x", resp)
			case 0x00: // 复位远方链路
				// 子站回应：0x10 0x00 0x01 0x01 0x16
				resp := buildFixedFrame(0x00)
				c.Write(resp)
				log.Printf("[mocksvr-iec101] >>> reset ok % x", resp)
			default:
				log.Printf("[mocksvr-iec101]    unknown fixed FC=0x%02X, ack", fc)
				// 默认: 简单 E5 单字节确认
				c.Write([]byte{0xE5})
			}

		case startVariable:
			// 拼头部
			headRest := []byte{first[0]}
			hr := make([]byte, 3)
			if _, err := io.ReadFull(c, hr); err != nil {
				return
			}
			headRest = append(headRest, hr...)
			if headRest[1] != headRest[2] || headRest[3] != startVariable {
				log.Printf("[mocksvr-iec101] bad variable header % x", headRest)
				return
			}
			bodyLen := int(headRest[1])
			body := make([]byte, bodyLen+2)
			if _, err := io.ReadFull(c, body); err != nil {
				return
			}
			if body[bodyLen+1] != endByte {
				log.Printf("[mocksvr-iec101] bad var end % x", body)
				return
			}
			if calcCS(body[:bodyLen]) != body[bodyLen] {
				log.Printf("[mocksvr-iec101] CS mismatch in var % x", body)
				return
			}
			control := body[0]
			asdu := body[2:bodyLen]
			log.Printf("[mocksvr-iec101] <<< variable C=0x%02X ASDU=% x", control, asdu)

			// ASDU: type(1)+VSQ(1)+COT(2)+CA(1)+IOA(2)+...
			if len(asdu) < 6 {
				log.Printf("[mocksvr-iec101] asdu too short")
				continue
			}
			typeID := asdu[0]
			cot := asdu[2]
			ca := asdu[4]

			// 仅处理总召唤 (type = 0x64, COT = 0x06 = 激活)
			if typeID == typeGI && cot == 0x06 {
				// (1) 链路层确认
				c.Write(buildFixedFrame(0x00))

				// (2) 总召唤确认
				giconf := buildGIConfirm(ca)
				c.Write(giconf)
				log.Printf("[mocksvr-iec101] >>> GI confirm % x", giconf)

				// (3) 遥信 8 组
				yc := buildYCValueGroup(ca, 0x01) // 从 0x01 起，与你文档"01 00"对齐
				c.Write(yc)
				log.Printf("[mocksvr-iec101] >>> YC 0x14 (%d bytes)", len(yc))

				// (4) 遥测 96 个 — 拆帧为多个子帧
				buildTelemetryFrames(c, ca, 0x95, 96)

				// (5) 终结
				end := buildGIEnd(ca)
				c.Write(end)
				log.Printf("[mocksvr-iec101] >>> GI end % x", end)

				continue
			}

			// 其他: 默认 E5
			c.Write([]byte{0xE5})

		default:
			log.Printf("[mocksvr-iec101] unknown first byte 0x%02X", first[0])
			c.Write([]byte{0xE5})
		}
	}
}

func main() {
	port := flag.Int("port", 8881, "TCP port to listen on (avoid conflict with gateway 2404/2405)")
	bind := flag.String("bind", "0.0.0.0", "bind address — use 0.0.0.0 to listen on all NICs (including WSL eth0 so Windows-side tools can connect); use 127.0.0.1 to restrict to loopback only")
	flag.Parse()

	addr := fmt.Sprintf("%s:%d", *bind, *port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen failed: %v", err)
	}
	defer l.Close()
	log.Printf("[mocksvr-iec101] listening on %s", addr)

	var wg sync.WaitGroup
	for {
		conn, err := l.Accept()
		if err != nil {
			log.Printf("accept failed: %v", err)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			serve(conn)
		}()
	}
}

// 保留: 编译器对 binary/import 无用警告的安抚。注意：这里只用 binary 但作占位。
var _ = binary.LittleEndian
var _ = os.Stdout
