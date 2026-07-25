// cmd/fake-iec101/main.go - IEC 60870-5-101 子站 (RTU) 全功能模拟器
//
// 与 cmd/mocksvr-iec101 不同：本 fake 是「全功能演示版本」，会周期性地推送
// 各类 ASDU 数据，目的是验证 gateway 端 handler 在所有 type_id 上都能正确拆位。
//
// 行为约定（与 mocksvr-iec101 共用同帧 layout）：
//   1. 监听 TCP（默认 8881）；先停 mocksvr 再跑本程序，避免端口冲突。
//   2. 响应 RESET_REMOTE_LINK (FC=0x00) → 回 ack
//   3. 响应 general interrogation (type=0x64, COT=0x06/ACTIVATION)
//      → 顺序推送一批：GI confirm + M_PS_NA_1(20)+M_SP_NA_1(1)+M_DP_NA_1(3)
//        +M_ST_NA_1(5)+M_BO_NA_1(7)+M_ME_NA_1(9)+M_ME_NB_1(11)+M_ME_NC_1(13)
//        +M_IT_NA_1(15)+M_SP_TA_1(2/带时标) + GI end
//   4. 周期性 SOE burst：每 5s 推一组带时标的 M_SP_TA_1(2)、M_DP_NA_1(3)
//      （COT=3 自发突发）— 验证 handler 接收突发而不只是 GI 模式
//
// 链路 / IOA 取值：
//   - linkAddr = 0x01, CA = 0x01
//   - M_PS_NA_1: IOA 0x01..0x80 (bit-packed, 16 组 × 16 点 = 128 遥信)
//   - M_SP_NA_1: IOA 0x81..0x96 (22 单点)
//   - M_DP_NA_1: IOA 0xA1..0xB0 (16 双点)
//   - M_ST_NA_1: IOA 0xC1..0xC8 (8 步位置)
//   - M_BO_NA_1: IOA 0xD1..0xD4 (4 个 32bit bit string)
//   - M_ME_NA_1: IOA 0x95..0xB4 (32 归一化值, -100..100 %)
//   - M_ME_NB_1: IOA 0xC1..0xE0 (32 标度化值, -32768..32767）
//   - M_ME_NC_1: IOA 0x101..0x120 (32 短浮点)
//   - M_IT_NA_1:  IOA 0x141..0x150 (16 累积量)
//   - M_SP_TA_1:  IOA 0x181..0x190 (16 单点带时标)
//
// 用法：
//   go run ./cmd/fake-iec101/ -port 8881
//   go run ./cmd/fake-iec101/ -port 8881 -burst=false   # 关掉周期性突发
//   go run ./cmd/fake-iec101/ -port 8881 -once           # GI 一次后断开（与 mocksvr 等价）
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ──── 帧常量 ────────────────────────────────────────────────────
const (
	startFixed    byte = 0x10
	startVariable byte = 0x68
	endByte       byte = 0x16
	linkAddr      byte = 0x01
	ca            byte = 0x01

	// ASDU type IDs
	typeSP    byte = 1
	typeSPTA  byte = 2
	typeDP    byte = 3
	typeSTA   byte = 5
	typeBO    byte = 7
	typeME_NA byte = 9
	typeME_NB byte = 11
	typeME_NC byte = 13
	typeIT    byte = 15
	typePS    byte = 20
	typeGI    byte = 100

	// COT
	cotSpont   uint16 = 3
	cotAct     uint16 = 6
	cotActCon  uint16 = 7
	cotGIEnd   uint16 = 10
	cotGIRsp   uint16 = 20
	cotInitEnd uint16 = 4
	cotReq     uint16 = 5

	qdsOK byte = 0x00
)

var burstTick atomic.Int64 // 突发 counter（调试用）

// ──── 帧组装 helpers ──────────────────────────────────────────

func calcCS(data []byte) byte { var s byte; for _, b := range data { s += b }; return s }

// 10 | C | A | CS | 16
func buildFixedFrame(c byte) []byte {
	return []byte{startFixed, c, linkAddr, calcCS([]byte{c, linkAddr}), endByte}
}

// 68 | L | L | 68 | C | A | ASDU | CS | 16
func buildVariableFrame(c byte, asdu []byte) []byte {
	userLen := 2 + len(asdu)
	if userLen < 3 || userLen > 0xFF {
		return nil
	}
	buf := make([]byte, 0, 4+userLen+2)
	buf = append(buf, startVariable, byte(userLen), byte(userLen), startVariable)
	buf = append(buf, c, linkAddr)
	buf = append(buf, asdu...)
	cs := calcCS(buf[4 : 4+2+len(asdu)])
	buf = append(buf, cs, endByte)
	return buf
}

// ASDU 头：type(1) + vsq(1) + cot(2 LE) + ca(1)
func asduH(typeID, vsq byte, cot uint16) []byte {
	b := []byte{typeID, vsq}
	cb := make([]byte, 2)
	binary.LittleEndian.PutUint16(cb, cot)
	b = append(b, cb...)
	b = append(b, ca)
	return b
}

func put16(b []byte, v uint16) []byte {
	c := make([]byte, 2)
	binary.LittleEndian.PutUint16(c, v)
	return append(b, c...)
}
func put32(b []byte, v uint32) []byte {
	c := make([]byte, 4)
	binary.LittleEndian.PutUint32(c, v)
	return append(b, c...)
}
func putFloat32(b []byte, v float32) []byte {
	return put32(b, math.Float32bits(v))
}

// ──── 各类 ASDU 构造（GI 响应路径） ─────────────────────

func buildGIConfirm() []byte {
	return buildVariableFrame(0x00, asduH(typeGI, 1, cotActCon))
}
func buildGIEnd() []byte {
	return buildVariableFrame(0x00, asduH(typeGI, 1, cotGIEnd))
}

// M_PS_NA_1 (20) — 成组单点：mocksvr 同样的 16 bit/组、SQ=0、每组 IOA+state+change+QDS
func buildPSNA1(ioaStart uint16, groups int) []byte {
	const perGroup = 7 // IOA(2)+state(2)+change(2)+QDS(1)
	infoObj := make([]byte, 0, perGroup*groups)
	for i := 0; i < groups; i++ {
		ioa := ioaStart + uint16(i*16)
		infoObj = put16(infoObj, ioa)
		state := uint16(0xA50A) // 交替 1010 0101 / 0000 1010
		if i%2 == 1 {
			state = 0x05A5
		}
		infoObj = append(infoObj, byte(state&0xFF), byte((state>>8)&0xFF))
		// change 2 字节（无业务语义）
		infoObj = append(infoObj, byte(0x00), byte(0x00))
		// QDS 1 字节
		infoObj = append(infoObj, qdsOK)
	}
	a := asduH(typePS, byte(groups), cotGIRsp)
	a = append(a, infoObj...)
	return buildVariableFrame(0x00, a)
}

// M_SP_NA_1 (1) — 单点，每点 IOA(2)+SIQ(1) = 3 字节
func buildSPNA1(ioaStart uint16, count int) []byte {
	infoObj := make([]byte, 0, 3*count)
	for i := 0; i < count; i++ {
		ioa := ioaStart + uint16(i)
		infoObj = put16(infoObj, ioa)
		siq := byte(0x01) // value=1
		if i%2 == 1 {
			siq = 0x00
		}
		infoObj = append(infoObj, siq)
	}
	a := asduH(typeSP, byte(count), cotGIRsp)
	a = append(a, infoObj...)
	return buildVariableFrame(0x00, a)
}

// M_DP_NA_1 (3) — 双点：IOA(2)+DPI(1) = 3 字节，DPI=低 2 bit 才有意义 (0..3)
func buildDPNA1(ioaStart uint16, count int) []byte {
	infoObj := make([]byte, 0, 3*count)
	for i := 0; i < count; i++ {
		ioa := ioaStart + uint16(i)
		infoObj = put16(infoObj, ioa)
		dpi := byte((i % 4) & 0x03)
		infoObj = append(infoObj, dpi)
	}
	a := asduH(typeDP, byte(count), cotGIRsp)
	a = append(a, infoObj...)
	return buildVariableFrame(0x00, a)
}

// M_ST_NA_1 (5) — 步位置：IOA(2)+VTI(1) = 3 字节
func buildSTNA1(ioaStart uint16, count int) []byte {
	infoObj := make([]byte, 0, 3*count)
	for i := 0; i < count; i++ {
		ioa := ioaStart + uint16(i)
		infoObj = put16(infoObj, ioa)
		vti := byte((i*3)&0x7F) | 0x00 // low 7 bit = value, bit 7 = transient
		infoObj = append(infoObj, vti)
	}
	a := asduH(typeSTA, byte(count), cotGIRsp)
	a = append(a, infoObj...)
	return buildVariableFrame(0x00, a)
}

// M_BO_NA_1 (7) — 32 位比特串：IOA(2)+BSI(4)+QDS(1) = 7 字节
func buildBONA1(ioaStart uint16, count int) []byte {
	infoObj := make([]byte, 0, 7*count)
	for i := 0; i < count; i++ {
		ioa := ioaStart + uint16(i)
		infoObj = put16(infoObj, ioa)
		infoObj = put32(infoObj, uint32(0xDEADBEEF)>>uint(i*4))
		infoObj = append(infoObj, qdsOK)
	}
	a := asduH(typeBO, byte(count), cotGIRsp)
	a = append(a, infoObj...)
	return buildVariableFrame(0x00, a)
}

// M_ME_NA_1 (9) — 归一化值：IOA(2)+NVA(2)+QDS(1) = 5 字节
// NVA 是带符号 16 bit (int16 LE)（-32768 = -1, 32767 ≈ +1）
func buildMENA1(ioaStart uint16, count int) []byte {
	infoObj := make([]byte, 0, 5*count)
	for i := 0; i < count; i++ {
		ioa := ioaStart + uint16(i)
		val := int16(i*200 - 3000) // [-3000..+3400]
		infoObj = put16(infoObj, ioa)
		infoObj = put16(infoObj, uint16(val))
		infoObj = append(infoObj, qdsOK)
	}
	a := asduH(typeME_NA, byte(count), cotGIRsp)
	a = append(a, infoObj...)
	return buildVariableFrame(0x00, a)
}

// M_ME_NB_1 (11) — 标度化值：同 NA_1 但无百分比映射，给原始 int16
func buildMENB1(ioaStart uint16, count int) []byte {
	infoObj := make([]byte, 0, 5*count)
	for i := 0; i < count; i++ {
		ioa := ioaStart + uint16(i)
		val := int16(i * 1000)
		infoObj = put16(infoObj, ioa)
		infoObj = put16(infoObj, uint16(val))
		infoObj = append(infoObj, qdsOK)
	}
	a := asduH(typeME_NB, byte(count), cotGIRsp)
	a = append(a, infoObj...)
	return buildVariableFrame(0x00, a)
}

// M_ME_NC_1 (13) — 短浮点：IOA(2)+float32(4)+QDS(1) = 7 字节
func buildMENC1(ioaStart uint16, count int) []byte {
	infoObj := make([]byte, 0, 7*count)
	for i := 0; i < count; i++ {
		ioa := ioaStart + uint16(i)
		f := float32(1.5 + float64(i)*0.7)
		infoObj = put16(infoObj, ioa)
		infoObj = putFloat32(infoObj, f)
		infoObj = append(infoObj, qdsOK)
	}
	a := asduH(typeME_NC, byte(count), cotGIRsp)
	a = append(a, infoObj...)
	return buildVariableFrame(0x00, a)
}

// M_IT_NA_1 (15) — 累积量：IOA(2)+BCR(4)+QDS(1) = 7 字节 (BCR 是 uint32)
func buildITNA1(ioaStart uint16, count int) []byte {
	infoObj := make([]byte, 0, 7*count)
	for i := 0; i < count; i++ {
		ioa := ioaStart + uint16(i)
		bcr := uint32(1_000_000 + i*17_300)
		infoObj = put16(infoObj, ioa)
		infoObj = put32(infoObj, bcr)
		infoObj = append(infoObj, qdsOK)
	}
	a := asduH(typeIT, byte(count), cotGIRsp)
	a = append(a, infoObj...)
	return buildVariableFrame(0x00, a)
}

// M_SP_TA_1 (2) — 单点带时标：IOA(2)+SIQ(1)+CP24Time2a(3) = 6 字节
// CP24Time2a: 3 字节 = millis-of-minute(uint24, ms)
// 这里用 frameIdx+counter 简单生成；不强制时标一定对，仅用于拆位测试
func buildSPTA1(ioaStart uint16, count int) []byte {
	infoObj := make([]byte, 0, 6*count)
	for i := 0; i < count; i++ {
		ioa := ioaStart + uint16(i)
		infoObj = put16(infoObj, ioa)
		siq := byte(0x01)
		if i%2 == 1 {
			siq = 0x00
		}
		infoObj = append(infoObj, siq)
		// CP24Time2a: 3 bytes LE = ((min * 60000 + sec*1000 + ms) & 0xFFFFFF)
		ms := (time.Now().Unix() % 60) * 60_000
		infoObj = append(infoObj, byte(ms&0xFF), byte((ms>>8)&0xFF), byte((ms>>16)&0xFF))
	}
	a := asduH(typeSPTA, byte(count), cotSpont) // 突发
	a = append(a, infoObj...)
	return buildVariableFrame(0x00, a)
}

// ──── 主连接 + 调度 ──────────────────────────────────────

func main() {
	port := flag.Int("port", 8881, "TCP 端口（默认 8881；与 mocksvr 同端口会冲突）")
	burst := flag.Bool("burst", true, "启动周期性 SOE 突发推送（每 5s 一次）")
	once := flag.Bool("once", false, "GI 后立即断开")
	flag.Parse()

	listenAddr := fmt.Sprintf(":%d", *port)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatalf("[fake-iec101] listen %s failed: %v", listenAddr, err)
	}
	log.Printf("[fake-iec101] listening on %s", listenAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[fake-iec101] accept error: %v", err)
			continue
		}
		log.Printf("[fake-iec101] connected from %s", conn.RemoteAddr())
		go handleConn(conn, *burst, *once)
	}
}

func handleConn(conn net.Conn, burst bool, once bool) {
	defer conn.Close()
	defer log.Printf("[fake-iec101] connection closed")

	var stopBurst atomic.Bool

	// burst: 5s 周期推一组 M_SP_TA_1
	if burst {
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					if stopBurst.Load() {
						return
					}
					frame := buildSPTA1(0x181, 16)
					// 突发用 bunch of M_SP_TA_1 + M_DP_NA_1 即可，仅为 handler 区分突发路径
					if _, err := conn.Write(frame); err != nil {
						log.Printf("[fake-iec101] burst write failed: %v", err)
						return
					}
					frame2 := buildDPNA1(0xA1, 4) // 小尾巴
					_, _ = conn.Write(frame2)
				}
			}
		}()
	}

	for {
		// 读首字节
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		first := make([]byte, 1)
		_, err := io.ReadFull(conn, first)
		if err != nil {
			if err == io.EOF {
				return
			}
			if e, ok := err.(net.Error); ok && e.Timeout() {
				if stopBurst.Load() {
					return
				}
				continue // 超时但未断开，继续等
			}
			log.Printf("[fake-iec101] read first byte failed: %v", err)
			return
		}

		switch first[0] {
		case startFixed:
			// 固定帧 4 字节后续 (C A CS 16)
			rest := make([]byte, 4)
			if _, err := io.ReadFull(conn, rest); err != nil {
				return
			}
			if rest[3] != endByte {
				return
			}
			if calcCS(rest[0:2]) != rest[2] {
				log.Printf("[fake-iec101] bad fixed frame CS: % x", rest)
				return
			}
			fc := rest[0]
			log.Printf("[fake-iec101] <<< fixed C=0x%02X", fc)
			switch fc {
			case 0x09:
				conn.Write(buildFixedFrame(0x0B)) // request respond
			case 0x00:
				conn.Write(buildFixedFrame(0x01)) // reset remote link ack
			default:
				conn.Write([]byte{0xE5})
			}
		case startVariable:
			headRest := []byte{first[0]}
			hr := make([]byte, 3)
			if _, err := io.ReadFull(conn, hr); err != nil {
				return
			}
			headRest = append(headRest, hr...)
			if headRest[1] != headRest[2] || headRest[3] != startVariable {
				return
			}
			bodyLen := int(headRest[1])
			body := make([]byte, bodyLen+2)
			if _, err := io.ReadFull(conn, body); err != nil {
				return
			}
			if body[bodyLen+1] != endByte {
				return
			}
			if calcCS(body[:bodyLen]) != body[bodyLen] {
				return
			}
			ctrl := body[0]
			asdu := body[2:bodyLen]
			if len(asdu) < 5 {
				continue
			}
			typeID := asdu[0]
			// COT 在 asdu[2..3]
			cot := binary.LittleEndian.Uint16(asdu[2:4])
			_ = ctrl
			log.Printf("[fake-iec101] <<< variable type=0x%02X cot=%d", typeID, cot)

			if typeID == typeGI && (cot == cotAct || cot == cotSpont) {
				// GI 启动
				conn.Write(buildFixedFrame(0x00))
				conn.Write(buildGIConfirm())

				// 顺序推送每 type demo set
				conn.Write(buildPSNA1(0x01, 8))     // 128 遥信
				conn.Write(buildSPNA1(0x81, 22))    // 22 单点
				conn.Write(buildDPNA1(0xA1, 16))    // 16 双点
				conn.Write(buildSTNA1(0xC1, 8))     // 8 步位置
				conn.Write(buildBONA1(0xD1, 4))     // 4 个 32bit bit string
				conn.Write(buildMENA1(0x95, 32))     // 32 归一化值
				conn.Write(buildMENB1(0x200, 30))   // 30 标度化值 (IOA 0x200..0x21D)
				conn.Write(buildMENC1(0x101, 32))   // 32 短浮点
				conn.Write(buildITNA1(0x141, 16))   // 16 累积量
				conn.Write(buildSPTA1(0x181, 16))   // 16 单点带时标

				conn.Write(buildGIEnd())

				if once {
					log.Printf("[fake-iec101] -once set; closing")
					stopBurst.Store(true)
					return
				}
				continue
			}

			// 其它类型：默认 E5
			conn.Write([]byte{0xE5})
		default:
			log.Printf("[fake-iec101] unknown first byte 0x%02X", first[0])
		}
	}
}

// guard code: imports 防 unused
var _ = sync.Mutex{}
