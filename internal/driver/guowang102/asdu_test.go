// internal/driver/guowang102/asdu_test.go - ASDU 编解码单元测试
package guowang102

import (
	"testing"
)

func TestParseASDU_Basic(t *testing.T) {
	// 构造一个标准 ASDU: TypeID=1, VSQ=1, COT=7, OA=0, CA=0xFFFF, RecordAddr=0, Payload=[...]
	payload := []byte{0x01, 0x02, 0x03, 0x04}
	asduData := BuildASDU(1, 0x01, 7, 0x00, 0xFFFF, 0x00, payload)

	parsed, err := ParseASDU(asduData)
	if err != nil {
		t.Fatalf("ParseASDU: %v", err)
	}

	if parsed.TypeID != 1 {
		t.Errorf("TypeID: want 1, got %d", parsed.TypeID)
	}
	if parsed.VSQ != 0x01 {
		t.Errorf("VSQ: want 0x01, got 0x%02X", parsed.VSQ)
	}
	if parsed.COT != 7 {
		t.Errorf("COT: want 7, got %d", parsed.COT)
	}
	if parsed.OriginAddr != 0x00 {
		t.Errorf("OA: want 0x00, got 0x%02X", parsed.OriginAddr)
	}
	if parsed.CommonAddr != 0xFFFF {
		t.Errorf("CA: want 0xFFFF, got 0x%04X", parsed.CommonAddr)
	}
	if parsed.RecordAddr != 0x00 {
		t.Errorf("RecordAddr: want 0x00, got 0x%02X", parsed.RecordAddr)
	}
	if len(parsed.Payload) != len(payload) {
		t.Errorf("Payload len: want %d, got %d", len(payload), len(parsed.Payload))
	}
	for i, b := range payload {
		if parsed.Payload[i] != b {
			t.Errorf("Payload[%d]: want 0x%02X, got 0x%02X", i, b, parsed.Payload[i])
		}
	}
}

func TestParseASDU_FileTransfer(t *testing.T) {
	// 模拟文件传输 ASDU: TypeID=139, 文件名 "TEST.WPD", 内容 "Hello World"
	fileName := "TEST.WPD"
	fileContent := []byte("Hello World")
	asduData := BuildFileTransferASDU(TypeID_File_EnergyPrediction, COT_FileNotLastFrame, fileName, fileContent)

	parsed, err := ParseASDU(asduData)
	if err != nil {
		t.Fatalf("ParseASDU: %v", err)
	}

	if parsed.TypeID != TypeID_File_EnergyPrediction {
		t.Errorf("TypeID: want 139, got %d", parsed.TypeID)
	}
	if parsed.COT != COT_FileNotLastFrame {
		t.Errorf("COT: want 0x08, got 0x%02X", parsed.COT)
	}

	// 解析文件内容
	name, content, isLast, err := ParseFileTransferASDU(parsed)
	if err != nil {
		t.Fatalf("ParseFileTransferASDU: %v", err)
	}

	if name != fileName {
		t.Errorf("FileName: want %q, got %q", fileName, name)
	}
	if len(content) != len(fileContent) {
		t.Errorf("Content len: want %d, got %d", len(fileContent), len(content))
	}
	for i, b := range fileContent {
		if content[i] != b {
			t.Errorf("Content[%d]: want 0x%02X, got 0x%02X", i, b, content[i])
		}
	}
	if isLast {
		t.Error("isLastFrame should be false for COT=0x08")
	}
}

func TestParseASDU_FileTransfer_LastFrame(t *testing.T) {
	fileName := "TEST.WPD"
	fileContent := []byte("Last chunk")
	asduData := BuildFileTransferASDU(TypeID_File_ShortTermPred, COT_FileLastFrame, fileName, fileContent)

	parsed, _ := ParseASDU(asduData)
	name, _, isLast, err := ParseFileTransferASDU(parsed)
	if err != nil {
		t.Fatalf("ParseFileTransferASDU: %v", err)
	}

	if name != fileName {
		t.Errorf("FileName: want %q, got %q", fileName, name)
	}
	if !isLast {
		t.Error("isLastFrame should be true for COT=0x07")
	}
}

func TestParseASDU_FileTransfer_NamePadding(t *testing.T) {
	// 文件名长度超过32字节应被截断
	longName := "THIS_IS_A_VERY_LONG_FILE_NAME_THAT_EXCEEDS_THIRTY_TWO_CHARACTERS.WPD"
	fileContent := []byte("content")
	asduData := BuildFileTransferASDU(TypeID_File_MastData, COT_FileNotLastFrame, longName, fileContent)

	parsed, _ := ParseASDU(asduData)
	name, _, _, err := ParseFileTransferASDU(parsed)
	if err != nil {
		t.Fatalf("ParseFileTransferASDU: %v", err)
	}

	// 应该被截断到32字节
	if len(name) > 32 {
		t.Errorf("FileName should be truncated to 32 chars, got %d", len(name))
	}
	expected := longName[:32]
	if name != expected {
		t.Errorf("FileName: want %q, got %q", expected, name)
	}
}

func TestBuildASDU_Roundtrip(t *testing.T) {
	original := BuildASDU(144, 0x01, 0x08, 0x00, 0xFFFF, 0x00, []byte{0xAA, 0xBB, 0xCC})
	parsed, err := ParseASDU(original)
	if err != nil {
		t.Fatalf("ParseASDU: %v", err)
	}

	rebuilt := BuildASDU(parsed.TypeID, parsed.VSQ, parsed.COT, parsed.OriginAddr, parsed.CommonAddr, parsed.RecordAddr, parsed.Payload)
	if len(rebuilt) != len(original) {
		t.Errorf("Rebuilt length mismatch: %d vs %d", len(rebuilt), len(original))
	}
	for i := range original {
		if rebuilt[i] != original[i] {
			t.Errorf("Byte[%d]: want 0x%02X, got 0x%02X", i, original[i], rebuilt[i])
		}
	}
}

func TestParseASDU_Errors(t *testing.T) {
	// 太短
	_, err := ParseASDU([]byte{0x01, 0x01})
	if err == nil {
		t.Error("expected error for too short ASDU")
	}

	// 空数据
	_, err = ParseASDU([]byte{})
	if err == nil {
		t.Error("expected error for empty ASDU")
	}
}

func TestParseIntegratedTotals_TypeID1(t *testing.T) {
	// TypeID=1 (M_IT_NA_1): 无时标，非序列模式
	// IOA=0x0001, Value=123456, SeqNum=1, QDS=0
	payload := []byte{
		0x01, 0x00, // IOA = 1 (小端)
		0x40, 0xE2, 0x01, 0x00, // Value = 123456 (小端)
		0x01, 0x00, // SeqNum = 1
		0x00, // QDS = 0
	}
	asdu := &ASDU{
		TypeID:     TypeID_M_IT_NA_1,
		VSQ:        0x01, // 非序列，1个对象
		COT:        COT_Spontaneous,
		OriginAddr: 0x00,
		CommonAddr: 0xFFFF,
		RecordAddr: 0x00,
		Payload:    payload,
	}

	results, err := ParseIntegratedTotals(asdu)
	if err != nil {
		t.Fatalf("ParseIntegratedTotals: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	it := results[0]
	if it.IOA != 1 {
		t.Errorf("IOA: want 1, got %d", it.IOA)
	}
	if it.Value != 123456 {
		t.Errorf("Value: want 123456, got %d", it.Value)
	}
	if it.SequenceNumber != 1 {
		t.Errorf("SeqNum: want 1, got %d", it.SequenceNumber)
	}
	if it.QDS != 0 {
		t.Errorf("QDS: want 0, got %d", it.QDS)
	}
}

func TestParseIntegratedTotals_TypeID2_WithTime24(t *testing.T) {
	// TypeID=2 (M_IT_TA_1): 带 CP24Time2a 时标 (3字节)
	// IOA=10, Value=1000, SeqNum=5, QDS=0, Time=0x123456
	payload := []byte{
		0x0A, 0x00, // IOA = 10
		0xE8, 0x03, 0x00, 0x00, // Value = 1000
		0x05, 0x00, // SeqNum = 5
		0x00, // QDS = 0
		0x34, 0x12, 0x56, // CP24Time2a: ms=0x1234, min=0x56 (小端存储)
	}
	asdu := &ASDU{
		TypeID:     TypeID_M_IT_TA_1,
		VSQ:        0x01,
		COT:        COT_Request,
		OriginAddr: 0x00,
		CommonAddr: 0xFFFF,
		RecordAddr: 0x00,
		Payload:    payload,
	}

	results, err := ParseIntegratedTotals(asdu)
	if err != nil {
		t.Fatalf("ParseIntegratedTotals: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	it := results[0]
	if it.IOA != 10 {
		t.Errorf("IOA: want 10, got %d", it.IOA)
	}
	if it.Value != 1000 {
		t.Errorf("Value: want 1000, got %d", it.Value)
	}
	if it.TimeTag24 == nil || len(it.TimeTag24) != 3 {
		t.Error("TimeTag24 should be present with 3 bytes")
	}
}

func TestParseIntegratedTotals_SequenceMode(t *testing.T) {
	// 序列模式: VSQ=0x81 (序列+1个对象), 第一个对象带IOA，后续只有数据
	// 但这里测试单对象序列模式
	asdu := &ASDU{
		TypeID:     TypeID_M_IT_NA_1,
		VSQ:        0x81, // 序列模式，1个对象
		COT:        COT_Background,
		OriginAddr: 0x00,
		CommonAddr: 0xFFFF,
		RecordAddr: 0x00,
		// 序列模式下第一个对象也带IOA
		Payload: []byte{
			0x01, 0x00, // IOA = 1
			0x40, 0xE2, 0x01, 0x00, // Value = 123456
			0x01, 0x00, // SeqNum = 1
			0x00, // QDS = 0
		},
	}

	results, err := ParseIntegratedTotals(asdu)
	if err != nil {
		t.Fatalf("ParseIntegratedTotals: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].IOA != 1 {
		t.Errorf("IOA: want 1, got %d", results[0].IOA)
	}
}

func TestParseIntegratedTotals_MultipleObjects(t *testing.T) {
	// 非序列模式，多个对象
	payload := []byte{
		// 对象1
		0x01, 0x00, // IOA=1
		0x00, 0x00, 0x00, 0x00, // Value=0
		0x01, 0x00, // SeqNum=1
		0x00, // QDS=0
		// 对象2
		0x02, 0x00, // IOA=2
		0x01, 0x00, 0x00, 0x00, // Value=1
		0x02, 0x00, // SeqNum=2
		0x00, // QDS=0
		// 对象3
		0x03, 0x00, // IOA=3
		0x02, 0x00, 0x00, 0x00, // Value=2
		0x03, 0x00, // SeqNum=3
		0x00, // QDS=0
	}
	asdu := &ASDU{
		TypeID:     TypeID_M_IT_NA_1,
		VSQ:        0x03, // 3个对象，非序列
		COT:        COT_GeneralInterrog,
		OriginAddr: 0x00,
		CommonAddr: 0xFFFF,
		RecordAddr: 0x00,
		Payload:    payload,
	}

	results, err := ParseIntegratedTotals(asdu)
	if err != nil {
		t.Fatalf("ParseIntegratedTotals: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	for i, it := range results {
		expectedIOA := uint16(i + 1)
		expectedValue := uint32(i)
		expectedSeq := uint16(i + 1)

		if it.IOA != expectedIOA {
			t.Errorf("Object %d: IOA want %d, got %d", i, expectedIOA, it.IOA)
		}
		if it.Value != expectedValue {
			t.Errorf("Object %d: Value want %d, got %d", i, expectedValue, it.Value)
		}
		if it.SequenceNumber != expectedSeq {
			t.Errorf("Object %d: SeqNum want %d, got %d", i, expectedSeq, it.SequenceNumber)
		}
	}
}

func TestTypeIDString(t *testing.T) {
	tests := []struct {
		typeID uint8
		want   string
	}{
		{1, "M_IT_NA_1 (Integrated Totals)"},
		{139, "File_EnergyPrediction (139)"},
		{144, "File_ShortTermPred (144)"},
		{145, "File_UltraShortTermPred (145)"},
		{146, "File_MastData (146)"},
		{147, "File_UnitStatus (147)"},
		{148, "File_Reserved_148"},
		{154, "File_Reserved_154"},
		{255, "Unknown_255"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := TypeIDString(tt.typeID)
			if got != tt.want {
				t.Errorf("TypeIDString(%d): want %q, got %q", tt.typeID, tt.want, got)
			}
		})
	}
}

func TestCOTString(t *testing.T) {
	tests := []struct {
		cot  uint8
		want string
	}{
		{1, "Periodic (1)"},
		{3, "Spontaneous (3)"},
		{13, "GeneralInterrog (13)/FileDuplicate(0x0D)"},
		{0x07, "ActivationCon/ActivationCon(7)/FileLastFrame(0x07)"},
		{0x08, "Deactivation (8)/FileNotLastFrame(0x08)"},
		{0x0A, "ActivationTerm (10)/FileRecvComplete(0x0A)"},
		{0x0B, "ReturnInfoRemote (11)/FileLenMatch(0x0B)"},
		{0x0C, "ReturnInfoLocal (12)/FileLenMismatch(0x0C)"},
		{0x0D, "GeneralInterrog (13)/FileDuplicate(0x0D)"},
		{0x0F, "FileTooLong (0x0F)"},
		{0x11, "FileNameInvalid (0x11)"},
		{0x14, "FrameLongConfirmed (0x14)"},
		{0xFF, "Unknown(0xFF)"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := COTString(tt.cot)
			if got != tt.want {
				t.Errorf("COTString(0x%02X): want %q, got %q", tt.cot, tt.want, got)
			}
		})
	}
}

func TestIsFileTransferTypeID(t *testing.T) {
	if !IsFileTransferTypeID(139) {
		t.Error("139 should be file transfer")
	}
	if !IsFileTransferTypeID(147) {
		t.Error("147 should be file transfer")
	}
	if !IsFileTransferTypeID(154) {
		t.Error("154 should be file transfer")
	}
	if IsFileTransferTypeID(1) {
		t.Error("1 should NOT be file transfer")
	}
	if IsFileTransferTypeID(101) {
		t.Error("101 should NOT be file transfer")
	}
}

func TestIsStandardTypeID(t *testing.T) {
	if !IsStandardTypeID(1) {
		t.Error("1 should be standard")
	}
	if !IsStandardTypeID(6) {
		t.Error("6 should be standard")
	}
	if !IsStandardTypeID(101) {
		t.Error("101 should be standard")
	}
	if IsStandardTypeID(139) {
		t.Error("139 should NOT be standard")
	}
}

func TestASDUString(t *testing.T) {
	asdu := &ASDU{
		TypeID:     139,
		VSQ:        0x01,
		COT:        0x08,
		OriginAddr: 0x00,
		CommonAddr: 0xFFFF,
		RecordAddr: 0x00,
		Payload:    []byte{0x01, 0x02, 0x03},
	}
	s := asdu.String()
	if s == "" {
		t.Error("String() should not be empty")
	}
	t.Logf("ASDU String: %s", s)
}

// BenchmarkParseASDU 基准测试
func BenchmarkParseASDU(b *testing.B) {
	asduData := BuildFileTransferASDU(139, 0x08, "TEST.WPD", make([]byte, 200))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ParseASDU(asduData)
	}
}

func BenchmarkBuildFileTransferASDU(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BuildFileTransferASDU(139, 0x08, "TEST.WPD", make([]byte, 200))
	}
}