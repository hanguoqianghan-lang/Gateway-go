// internal/driver/guowang102/asdu.go - 国网102规约 ASDU 编解码
package guowang102

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ─────────────────────────────────────────────────────────────────────────────
// 类型标识 (TypeID) - 国网102扩展
// ─────────────────────────────────────────────────────────────────────────────

const (
	// 标准 IEC102 电能量类型
	TypeID_M_IT_NA_1  = 1  // 累计值
	TypeID_M_IT_TA_1  = 2  // 累计值带时标 CP24Time2a
	TypeID_M_IT_NB_1  = 3  // 累计值带时标 CP24Time2a (扩展)
	TypeID_M_IT_TB_1  = 4  // 累计值带时标 CP56Time2a
	TypeID_M_IT_NC_1  = 5  // 累计值带时标 CP56Time2a (扩展)
	TypeID_M_IT_TC_1  = 6  // 累计值带时标 CP56Time2a (扩展)

	// 系统命令
	TypeID_C_CI_NA_1 = 101 // 计数量召唤命令
	TypeID_C_RD_NA_1 = 102 // 读命令

	// 国网102扩展文件传输类型 (0x8B-0x9A)
	TypeID_File_EnergyPrediction   = 139 // 0x8B 电量预测文件
	TypeID_File_ShortTermPred      = 144 // 0x90 短期预测文件
	TypeID_File_UltraShortTermPred = 145 // 0x91 超短期预测文件
	TypeID_File_MastData           = 146 // 0x92 测风/测光数据文件
	TypeID_File_UnitStatus         = 147 // 0x93 机组/逆变器状态数据文件
	TypeID_File_Reserved148        = 148 // 0x94 保留
	TypeID_File_Reserved149        = 149 // 0x95 保留
	TypeID_File_Reserved150        = 150 // 0x96 保留
	TypeID_File_Reserved151        = 151 // 0x97 保留
	TypeID_File_Reserved152        = 152 // 0x98 保留
	TypeID_File_Reserved153        = 153 // 0x99 保留
	TypeID_File_Reserved154        = 154 // 0x9A 保留
)

// ─────────────────────────────────────────────────────────────────────────────
// 传送原因 (COT) - 标准 + 国网102扩展文件传输
// ─────────────────────────────────────────────────────────────────────────────

const (
	// 标准 COT
	COT_Periodic          = 1  // 周期/循环
	COT_Background        = 2  // 背景扫描
	COT_Spontaneous       = 3  // 突发/自发
	COT_Initialized       = 4  // 初始化
	COT_Request           = 5  // 请求
	COT_Activation        = 6  // 激活
	COT_ActivationCon     = 7  // 激活确认
	COT_Deactivation      = 8  // 失效
	COT_DeactivationCon   = 9  // 失效确认
	COT_ActivationTerm    = 10 // 激活终止
	COT_ReturnInfoRemote  = 11 // 远方命令返回信息
	COT_ReturnInfoLocal   = 12 // 就地命令返回信息
	COT_GeneralInterrog   = 13 // 总召唤
	COT_GeneralInterrogEnd = 14 // 总召唤结束

	// 国网102扩展文件传输 COT (0x07-0x14)
	COT_FileLastFrame      = 0x07 // 文件最后一帧，传输结束
	COT_FileNotLastFrame   = 0x08 // 非最后一帧，文件未结束
	COT_FileRecvComplete   = 0x0A // 主站确认文件接收结束
	COT_FileLenMatch       = 0x0B // 子站确认长度一致，处理文件
	COT_FileLenMismatch    = 0x0C // 子站长度不一致，准备重传
	COT_FileDuplicate      = 0x0D // 主站检测到重复传输
	COT_FileDupConfirmed   = 0x0E // 子站确认重复，作其他处理
	COT_FileTooLong        = 0x0F // 主站认为文件过长 (>512*40)
	COT_FileLongConfirmed  = 0x10 // 子站确认文件过长
	COT_FileNameInvalid    = 0x11 // 主站认为文件名格式错误
	COT_FileNameConfirmed  = 0x12 // 子站确认文件名格式错误
	COT_FrameTooLong       = 0x13 // 主站认为单帧过长
	COT_FrameLongConfirmed = 0x14 // 子站确认单帧过长
)

// ─────────────────────────────────────────────────────────────────────────────
// ASDU 结构定义
// ─────────────────────────────────────────────────────────────────────────────

// ASDU 应用服务数据单元
type ASDU struct {
	TypeID      uint8  // 类型标识
	VSQ         uint8  // 可变结构限定词 (固定 0x01)
	COT         uint8  // 传送原因
	OriginAddr  uint8  // 源发地址 (OA，通常 0x00)
	CommonAddr  uint16 // 公共地址 (CA，固定 0xFFFF)
	RecordAddr  uint8  // 记录地址 (固定 0x00)
	Payload     []byte // 数据区 (文件名32字节 + 文件内容，或其他信息体)
	Sequence    uint16 // 帧序号 (用于文件分帧重组，非标准字段，内部使用)
}

// 标准 ASDU 头部固定长度
const ASDUHeaderLen = 6 // TypeID(1) + VSQ(1) + COT(1) + OA(1) + CA(2)

// ─────────────────────────────────────────────────────────────────────────────
// ASDU 编解码
// ─────────────────────────────────────────────────────────────────────────────

// ParseASDU 解析 ASDU
func ParseASDU(data []byte) (*ASDU, error) {
	if len(data) < ASDUHeaderLen {
		return nil, fmt.Errorf("%w: need at least %d bytes, got %d", ErrASDUTooShort, ASDUHeaderLen, len(data))
	}

	asdu := &ASDU{
		TypeID:     data[0],
		VSQ:        data[1],
		COT:        data[2],
		OriginAddr: data[3],
		CommonAddr: uint16(data[4]) | (uint16(data[5]) << 8),
		RecordAddr: 0x00, // 默认值，如果数据区有则后续解析
	}

	// 数据区从第6字节开始
	if len(data) > ASDUHeaderLen {
		// 记录地址通常在数据区第一字节
		asdu.RecordAddr = data[ASDUHeaderLen]
		asdu.Payload = data[ASDUHeaderLen+1:]
	} else {
		asdu.Payload = nil
	}

	return asdu, nil
}

// BuildASDU 构建 ASDU
func BuildASDU(typeID, vsq, cot, oa uint8, ca uint16, recordAddr uint8, payload []byte) []byte {
	totalLen := ASDUHeaderLen + 1 + len(payload) // 头部6 + 记录地址1 + payload
	buf := make([]byte, totalLen)

	buf[0] = typeID
	buf[1] = vsq
	buf[2] = cot
	buf[3] = oa
	buf[4] = byte(ca & 0xFF)
	buf[5] = byte((ca >> 8) & 0xFF)
	buf[6] = recordAddr
	copy(buf[7:], payload)

	return buf
}

// BuildFileTransferASDU 构建文件传输 ASDU
// 文件名固定32字节 (左对齐，右侧补0)，后跟文件内容
func BuildFileTransferASDU(typeID, cot uint8, fileName string, fileContent []byte) []byte {
	// 文件名处理：截断或补齐到32字节
	nameBytes := make([]byte, 32)
	copy(nameBytes, []byte(fileName))

	payload := make([]byte, 32+len(fileContent))
	copy(payload[:32], nameBytes)
	copy(payload[32:], fileContent)

	return BuildASDU(typeID, 0x01, cot, 0x00, DefaultCommonAddress, 0x00, payload)
}

// ParseFileTransferASDU 解析文件传输 ASDU
// 返回：文件名(去除末尾空字符), 文件内容, 是否最后一帧
func ParseFileTransferASDU(asdu *ASDU) (fileName string, content []byte, isLastFrame bool, err error) {
	if len(asdu.Payload) < 32 {
		return "", nil, false, fmt.Errorf("%w: payload too short for file name (need 32, got %d)", ErrASDUTooShort, len(asdu.Payload))
	}

	// 提取文件名 (32字节，去除尾部空字符)
	nameBytes := asdu.Payload[:32]
	fileName = string(nameBytes)
	// 去除尾部的 \x00 和空格
	for i := len(fileName) - 1; i >= 0; i-- {
		if fileName[i] == '\x00' || fileName[i] == ' ' {
			fileName = fileName[:i]
		} else {
			break
		}
	}

	content = asdu.Payload[32:]
	isLastFrame = (asdu.COT == COT_FileLastFrame)

	return fileName, content, isLastFrame, nil
}

// BuildFileTransferACK 构建文件传输应答 ASDU (COT=0x0A 等)
func BuildFileTransferACK(typeID uint8, commonAddr uint16, cot uint8, originAddr uint8) []byte {
	// 空 payload
	payload := make([]byte, 0)
	return BuildASDU(typeID, 0x01, cot, originAddr, commonAddr, 0x00, payload)
}

// ─────────────────────────────────────────────────────────────────────────────
// 信息体解析辅助 (标准 IEC102 电能量)
// ─────────────────────────────────────────────────────────────────────────────

// IntegratedTotal 累计值信息体
type IntegratedTotal struct {
	IOA             uint16 // 信息对象地址
	Value           uint32 // 电能量值
	SequenceNumber  uint16 // 序列号
	QDS             uint8  // 质量描述符
	TimeTag24       []byte // CP24Time2a (3字节，可选)
	TimeTag56       []byte // CP56Time2a (7字节，可选)
}

// ParseIntegratedTotals 解析累计值 ASDU (TypeID 1-6)
// 支持序列模式 (VSQ&0x80=1) 和非序列模式
func ParseIntegratedTotals(asdu *ASDU) ([]IntegratedTotal, error) {
	if asdu.TypeID < 1 || asdu.TypeID > 6 {
		return nil, fmt.Errorf("invalid TypeID for integrated totals: %d", asdu.TypeID)
	}

	count := int(asdu.VSQ & 0x7F)
	isSequence := (asdu.VSQ & 0x80) != 0

	results := make([]IntegratedTotal, 0, count)
	offset := 0
	var baseIOA uint16

	for i := 0; i < count; i++ {
		var it IntegratedTotal

		if isSequence && i > 0 {
			// 序列模式：后续对象只有数据部分，IOA 递增
			it.IOA = baseIOA + uint16(i)
			// 数据格式：Value(4) + SeqNum(2) + QDS(1) [+ TimeTag]
			dataLen := 7
			if asdu.TypeID >= 4 { // CP56Time2a
				dataLen += 7
			} else if asdu.TypeID >= 2 { // CP24Time2a
				dataLen += 3
			}

			if offset+dataLen > len(asdu.Payload) {
				return nil, fmt.Errorf("sequence mode: insufficient data for object %d", i)
			}

			it.Value = binary.LittleEndian.Uint32(asdu.Payload[offset : offset+4])
			it.SequenceNumber = binary.LittleEndian.Uint16(asdu.Payload[offset+4 : offset+6])
			it.QDS = asdu.Payload[offset+6]
			offset += 7

			if asdu.TypeID >= 2 && asdu.TypeID <= 3 { // CP24Time2a
				if offset+3 <= len(asdu.Payload) {
					it.TimeTag24 = asdu.Payload[offset : offset+3]
					offset += 3
				}
			} else if asdu.TypeID >= 4 { // CP56Time2a
				if offset+7 <= len(asdu.Payload) {
					it.TimeTag56 = asdu.Payload[offset : offset+7]
					offset += 7
				}
			}
		} else {
			// 非序列模式或第一个对象：完整信息体
			// 格式：IOA(2) + Value(4) + SeqNum(2) + QDS(1) [+ TimeTag]
			dataLen := 9
			if asdu.TypeID >= 4 {
				dataLen += 7
			} else if asdu.TypeID >= 2 {
				dataLen += 3
			}

			if offset+dataLen > len(asdu.Payload) {
				return nil, fmt.Errorf("non-sequence mode: insufficient data for object %d", i)
			}

			it.IOA = binary.LittleEndian.Uint16(asdu.Payload[offset : offset+2])
			it.Value = binary.LittleEndian.Uint32(asdu.Payload[offset+2 : offset+6])
			it.SequenceNumber = binary.LittleEndian.Uint16(asdu.Payload[offset+6 : offset+8])
			it.QDS = asdu.Payload[offset+8]
			offset += 9

			if asdu.TypeID >= 2 && asdu.TypeID <= 3 { // CP24Time2a
				if offset+3 <= len(asdu.Payload) {
					it.TimeTag24 = asdu.Payload[offset : offset+3]
					offset += 3
				}
			} else if asdu.TypeID >= 4 { // CP56Time2a
				if offset+7 <= len(asdu.Payload) {
					it.TimeTag56 = asdu.Payload[offset : offset+7]
					offset += 7
				}
			}

			if i == 0 {
				baseIOA = it.IOA
			}
		}

		results = append(results, it)
	}

	return results, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// 错误定义
// ─────────────────────────────────────────────────────────────────────────────

var (
	ErrASDUTooShort = errors.New("asdu too short")
	ErrInvalidTypeID = errors.New("invalid type identification")
	ErrInvalidCOT = errors.New("invalid cause of transmission")
)

// ─────────────────────────────────────────────────────────────────────────────
// 工具函数
// ─────────────────────────────────────────────────────────────────────────────

// IsFileTransferTypeID 判断是否为文件传输类型标识
func IsFileTransferTypeID(typeID uint8) bool {
	return typeID >= TypeID_File_EnergyPrediction && typeID <= TypeID_File_Reserved154
}

// IsStandardTypeID 判断是否为标准 IEC102 类型标识
func IsStandardTypeID(typeID uint8) bool {
	return (typeID >= 1 && typeID <= 6) || typeID == TypeID_C_CI_NA_1 || typeID == TypeID_C_RD_NA_1
}

// TypeIDString 类型标识转字符串
func TypeIDString(typeID uint8) string {
	switch typeID {
	case TypeID_M_IT_NA_1:
		return "M_IT_NA_1 (Integrated Totals)"
	case TypeID_M_IT_TA_1:
		return "M_IT_TA_1 (Integrated Totals with CP24Time2a)"
	case TypeID_M_IT_NB_1:
		return "M_IT_NB_1 (Integrated Totals with CP24Time2a ext)"
	case TypeID_M_IT_TB_1:
		return "M_IT_TB_1 (Integrated Totals with CP56Time2a)"
	case TypeID_M_IT_NC_1:
		return "M_IT_NC_1 (Integrated Totals with CP56Time2a ext)"
	case TypeID_M_IT_TC_1:
		return "M_IT_TC_1 (Integrated Totals with CP56Time2a ext)"
	case TypeID_C_CI_NA_1:
		return "C_CI_NA_1 (Counter Interrogation)"
	case TypeID_C_RD_NA_1:
		return "C_RD_NA_1 (Read Command)"
	case TypeID_File_EnergyPrediction:
		return "File_EnergyPrediction (139)"
	case TypeID_File_ShortTermPred:
		return "File_ShortTermPred (144)"
	case TypeID_File_UltraShortTermPred:
		return "File_UltraShortTermPred (145)"
	case TypeID_File_MastData:
		return "File_MastData (146)"
	case TypeID_File_UnitStatus:
		return "File_UnitStatus (147)"
	default:
		if typeID >= 148 && typeID <= 154 {
			return fmt.Sprintf("File_Reserved_%d", typeID)
		}
		return fmt.Sprintf("Unknown_%d", typeID)
	}
}

// COTString 传送原因转字符串
// 注意：标准 COT 和文件传输 COT 共用 7-14 的数值，需结合 TypeID 判断
func COTString(cot uint8) string {
	switch cot {
	case COT_Periodic:
		return "Periodic (1)"
	case COT_Background:
		return "Background (2)"
	case COT_Spontaneous:
		return "Spontaneous (3)"
	case COT_Initialized:
		return "Initialized (4)"
	case COT_Request:
		return "Request (5)"
	case COT_Activation:
		return "Activation (6)"
	case COT_ActivationCon:
		return "ActivationCon/ActivationCon(7)/FileLastFrame(0x07)"
	case COT_Deactivation:
		return "Deactivation (8)/FileNotLastFrame(0x08)"
	case COT_DeactivationCon:
		return "DeactivationCon (9)"
	case COT_ActivationTerm:
		return "ActivationTerm (10)/FileRecvComplete(0x0A)"
	case COT_ReturnInfoRemote:
		return "ReturnInfoRemote (11)/FileLenMatch(0x0B)"
	case COT_ReturnInfoLocal:
		return "ReturnInfoLocal (12)/FileLenMismatch(0x0C)"
	case COT_GeneralInterrog:
		return "GeneralInterrog (13)/FileDuplicate(0x0D)"
	case COT_GeneralInterrogEnd:
		return "GeneralInterrogEnd (14)/FileDupConfirmed(0x0E)"
	case COT_FileTooLong:
		return "FileTooLong (0x0F)"
	case COT_FileLongConfirmed:
		return "FileLongConfirmed (0x10)"
	case COT_FileNameInvalid:
		return "FileNameInvalid (0x11)"
	case COT_FileNameConfirmed:
		return "FileNameConfirmed (0x12)"
	case COT_FrameTooLong:
		return "FrameTooLong (0x13)"
	case COT_FrameLongConfirmed:
		return "FrameLongConfirmed (0x14)"
	default:
		return fmt.Sprintf("Unknown(0x%02X)", cot)
	}
}

// String ASDU 字符串表示
func (a *ASDU) String() string {
	return fmt.Sprintf("ASDU{TypeID=%s(%d), VSQ=0x%02X, COT=%s(0x%02X), OA=0x%02X, CA=0x%04X, RecordAddr=0x%02X, PayloadLen=%d}",
		TypeIDString(a.TypeID), a.TypeID, a.VSQ, COTString(a.COT), a.COT, a.OriginAddr, a.CommonAddr, a.RecordAddr, len(a.Payload))
}