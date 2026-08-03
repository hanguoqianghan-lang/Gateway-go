// internal/driver/guowang102/frame_test.go - 帧编解码单元测试
package guowang102

import (
	"testing"
)

func TestCalcCS(t *testing.T) {
	// 测试校验和计算
	data := []byte{0x03, 0xFF, 0xFF} // C=0x03, A=0xFFFF
	cs := CalcCS(data)
	// 0x03 + 0xFF + 0xFF = 0x201 -> 低8位 = 0x01
	if cs != 0x01 {
		t.Errorf("CalcCS: want 0x01, got 0x%02X", cs)
	}

	// 空数据
	if CalcCS([]byte{}) != 0x00 {
		t.Error("CalcCS empty: want 0x00")
	}

	// 溢出测试
	data2 := []byte{0xFF, 0xFF, 0xFF, 0xFF} // 4*255 = 1020 = 0x3FC -> 0xFC
	if CalcCS(data2) != 0xFC {
		t.Errorf("CalcCS overflow: want 0xFC, got 0x%02X", CalcCS(data2))
	}
}

func TestDownlinkControlEncodeDecode(t *testing.T) {
	tests := []struct {
		name string
		ctrl DownlinkControl
		want byte
	}{
		{"ResetLink FCV=0", DownlinkControl{FCB: false, FCV: false, FC: FC_RESET_REMOTE_LINK}, 0x40}, // PRM=1, FC=0
		{"ResetLink FCV=1", DownlinkControl{FCB: true, FCV: true, FC: FC_RESET_REMOTE_LINK}, 0x70},   // PRM=1, FCB=1, FCV=1, FC=0
		{"SendConfirm FCV=1", DownlinkControl{FCB: false, FCV: true, FC: FC_SEND_CONFIRM}, 0x53},      // PRM=1, FCV=1, FC=3
		{"SendConfirm FCB=1", DownlinkControl{FCB: true, FCV: true, FC: FC_SEND_CONFIRM}, 0x73},       // PRM=1, FCB=1, FCV=1, FC=3
		{"StartTransfer", DownlinkControl{FCB: false, FCV: false, FC: FC_START_DATA_TRANSFER}, 0x44},  // PRM=1, FC=4
		{"RequestLinkStatus", DownlinkControl{FCB: false, FCV: false, FC: FC_REQUEST_LINK_STATUS}, 0x49}, // PRM=1, FC=9
		{"RequestLevel1Data", DownlinkControl{FCB: false, FCV: true, FC: FC_REQUEST_LEVEL1_DATA}, 0x5A},  // PRM=1, FCV=1, FC=10
		{"RequestLevel2Data", DownlinkControl{FCB: true, FCV: true, FC: FC_REQUEST_LEVEL2_DATA}, 0x7B},   // PRM=1, FCB=1, FCV=1, FC=11
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := tt.ctrl.Encode()
			if encoded != tt.want {
				t.Errorf("Encode: want 0x%02X, got 0x%02X", tt.want, encoded)
			}

			decoded := DecodeDownlinkControl(encoded)
			if decoded.FCB != tt.ctrl.FCB {
				t.Errorf("FCB: want %v, got %v", tt.ctrl.FCB, decoded.FCB)
			}
			if decoded.FCV != tt.ctrl.FCV {
				t.Errorf("FCV: want %v, got %v", tt.ctrl.FCV, decoded.FCV)
			}
			if decoded.FC != tt.ctrl.FC {
				t.Errorf("FC: want %d, got %d", tt.ctrl.FC, decoded.FC)
			}
		})
	}
}

func TestUplinkControlEncodeDecode(t *testing.T) {
	tests := []struct {
		name string
		ctrl UplinkControl
		want byte
	}{
		{"ACK", UplinkControl{ACD: false, DFC: false, FC: 0}, 0x00},
		{"LinkBusy", UplinkControl{ACD: false, DFC: false, FC: 1}, 0x01},
		{"DataResponse", UplinkControl{ACD: false, DFC: false, FC: 8}, 0x08},
		{"NoData", UplinkControl{ACD: false, DFC: false, FC: 9}, 0x09},
		{"StatusResponse", UplinkControl{ACD: false, DFC: false, FC: 11}, 0x0B},
		{"WithACD", UplinkControl{ACD: true, DFC: false, FC: 0}, 0x40},
		{"WithDFC", UplinkControl{ACD: false, DFC: true, FC: 0}, 0x20},
		{"WithACD_DFC", UplinkControl{ACD: true, DFC: true, FC: 8}, 0x68},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := tt.ctrl.Encode()
			if encoded != tt.want {
				t.Errorf("Encode: want 0x%02X, got 0x%02X", tt.want, encoded)
			}

			decoded := DecodeUplinkControl(encoded)
			if decoded.ACD != tt.ctrl.ACD {
				t.Errorf("ACD: want %v, got %v", tt.ctrl.ACD, decoded.ACD)
			}
			if decoded.DFC != tt.ctrl.DFC {
				t.Errorf("DFC: want %v, got %v", tt.ctrl.DFC, decoded.DFC)
			}
			if decoded.FC != tt.ctrl.FC {
				t.Errorf("FC: want %d, got %d", tt.ctrl.FC, decoded.FC)
			}
		})
	}
}

func TestBuildFixedFrame(t *testing.T) {
	// 复位链路帧: 10H | 40H(PRM=1,FC=0) | FF FF(地址) | CS | 16H
	// CS = 0x40 + 0xFF + 0xFF = 0x23E -> 0x3E
	frame := BuildResetLink(DefaultLinkAddress)
	expected := []byte{0x10, 0x40, 0xFF, 0xFF, 0x3E, 0x16}
	if len(frame) != len(expected) {
		t.Fatalf("len: want %d, got %d", len(expected), len(frame))
	}
	for i, b := range expected {
		if frame[i] != b {
			t.Errorf("byte[%d]: want 0x%02X, got 0x%02X", i, b, frame[i])
		}
	}

	// 启动数据传输: FC=4
	frame2 := BuildStartDataTransfer(DefaultLinkAddress)
	// 10H | 44H(PRM=1,FC=4) | FF FF | CS | 16H
	// CS = 0x44 + 0xFF + 0xFF = 0x242 -> 0x42
	expected2 := []byte{0x10, 0x44, 0xFF, 0xFF, 0x42, 0x16}
	for i, b := range expected2 {
		if frame2[i] != b {
			t.Errorf("StartTransfer byte[%d]: want 0x%02X, got 0x%02X", i, b, frame2[i])
		}
	}
}

func TestBuildVariableFrame(t *testing.T) {
	// 发送确认数据帧，带ASDU
	asdu := []byte{0x01, 0x01, 0x07, 0xFF, 0xFF, 0x00, 0x48, 0x65, 0x6C, 0x6C, 0x6F} // 模拟ASDU
	frame := BuildSendConfirmData(DefaultLinkAddress, false, asdu)

	// 验证结构: 68H | L | L | 68H | C | A | A | ASDU | CS | 16H
	if frame[0] != StartByteVariable {
		t.Errorf("start byte: want 0x68, got 0x%02X", frame[0])
	}
	if frame[3] != StartByteVariable {
		t.Errorf("second start: want 0x68, got 0x%02X", frame[3])
	}
	// L = ASDU长度 + 3
	l := len(asdu) + 3
	if frame[1] != byte(l) || frame[2] != byte(l) {
		t.Errorf("L: want %d, got %d/%d", l, frame[1], frame[2])
	}
	// 控制域: FC=3, FCV=1, PRM=1 -> 0x53
	if frame[4] != 0x53 {
		t.Errorf("control: want 0x53, got 0x%02X", frame[4])
	}
	// 地址
	if frame[5] != 0xFF || frame[6] != 0xFF {
		t.Errorf("address: want 0xFFFF, got 0x%02X%02X", frame[6], frame[5])
	}
	// ASDU内容
	for i, b := range asdu {
		if frame[7+i] != b {
			t.Errorf("asdu[%d]: want 0x%02X, got 0x%02X", i, b, frame[7+i])
		}
	}
	// 结束符
	endIdx := len(frame) - 1
	if frame[endIdx] != EndByte {
		t.Errorf("end byte: want 0x16, got 0x%02X", frame[endIdx])
	}
	// 校验和验证
	csStart := 4
	csEnd := csStart + l
	expectedCS := CalcCS(frame[csStart:csEnd])
	if frame[csEnd] != expectedCS {
		t.Errorf("CS: want 0x%02X, got 0x%02X", expectedCS, frame[csEnd])
	}
}

func TestParseFixedFrame(t *testing.T) {
	// 复位链路帧
	data := []byte{0x10, 0x40, 0xFF, 0xFF, 0x3E, 0x16}
	frame, err := ParseFrame(data)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	if frame.Type != FrameTypeFixed {
		t.Errorf("type: want Fixed, got %v", frame.Type)
	}
	if frame.Control != 0x40 {
		t.Errorf("control: want 0x40, got 0x%02X", frame.Control)
	}
	if frame.Address != 0xFFFF {
		t.Errorf("address: want 0xFFFF, got 0x%04X", frame.Address)
	}
	if frame.ASDU != nil {
		t.Error("ASDU should be nil for fixed frame")
	}

	// 解析下行控制域
	dlCtrl := frame.GetDownlinkControl()
	if dlCtrl.FC != FC_RESET_REMOTE_LINK {
		t.Errorf("FC: want 0, got %d", dlCtrl.FC)
	}
	if dlCtrl.FCV {
		t.Error("FCV should be false for reset link")
	}
}

func TestParseFixedFrame_Errors(t *testing.T) {
	// 太短
	_, err := ParseFrame([]byte{0x10, 0x40})
	if err == nil {
		t.Error("expected error for short frame")
	}

	// 错误结束符
	_, err = ParseFrame([]byte{0x10, 0x40, 0xFF, 0xFF, 0x3E, 0x17})
	if err == nil {
		t.Error("expected error for invalid end byte")
	}

	// 校验和错误
	_, err = ParseFrame([]byte{0x10, 0x40, 0xFF, 0xFF, 0x00, 0x16}) // CS=0x00 wrong
	if err == nil {
		t.Error("expected error for CS mismatch")
	}
}

func TestParseVariableFrame(t *testing.T) {
	// 构造一个完整的可变帧
	asdu := []byte{0x01, 0x01, 0x07, 0xFF, 0xFF, 0x00}
	frameData := BuildSendConfirmData(DefaultLinkAddress, false, asdu)

	frame, err := ParseFrame(frameData)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	if frame.Type != FrameTypeVariable {
		t.Errorf("type: want Variable, got %v", frame.Type)
	}
	if frame.Control != 0x53 { // FC=3, FCV=1, PRM=1
		t.Errorf("control: want 0x53, got 0x%02X", frame.Control)
	}
	if frame.Address != 0xFFFF {
		t.Errorf("address: want 0xFFFF, got 0x%04X", frame.Address)
	}
	if len(frame.ASDU) != len(asdu) {
		t.Errorf("ASDU len: want %d, got %d", len(asdu), len(frame.ASDU))
	}
	for i, b := range asdu {
		if frame.ASDU[i] != b {
			t.Errorf("ASDU[%d]: want 0x%02X, got 0x%02X", i, b, frame.ASDU[i])
		}
	}
}

func TestParseVariableFrame_Errors(t *testing.T) {
	// L不匹配
	data := []byte{0x68, 0x05, 0x06, 0x68, 0x53, 0xFF, 0xFF, 0x00, 0x16}
	_, err := ParseFrame(data)
	if err == nil {
		t.Error("expected error for L mismatch")
	}

	// 无效第二个启动符
	data2 := []byte{0x68, 0x05, 0x05, 0x10, 0x53, 0xFF, 0xFF, 0x00, 0x16}
	_, err = ParseFrame(data2)
	if err == nil {
		t.Error("expected error for invalid second start byte")
	}

	// 太短
	_, err = ParseFrame([]byte{0x68, 0x05, 0x05})
	if err == nil {
		t.Error("expected error for too short")
	}

	// 校验和错误 - 修改ASDU中的一个字节
	asdu := []byte{0x01, 0x01, 0x07, 0xFF, 0xFF, 0x00}
	frameData := BuildSendConfirmData(DefaultLinkAddress, false, asdu)
	frameData[len(frameData)-3] ^= 0x01 // 破坏校验和前的数据
	_, err = ParseFrame(frameData)
	if err == nil {
		t.Error("expected error for CS mismatch")
	}
}

func TestParseSingleACK(t *testing.T) {
	data := []byte{0xE5}
	frame, err := ParseFrame(data)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	if frame.Type != FrameTypeSingleACK {
		t.Errorf("type: want SingleACK, got %v", frame.Type)
	}
}

func TestParseSingleACK_Errors(t *testing.T) {
	_, err := ParseFrame([]byte{0xE6})
	if err == nil {
		t.Error("expected error for invalid single byte")
	}
}

func TestFrameTypeDetection(t *testing.T) {
	// 下行帧
	downlink := BuildResetLink(DefaultLinkAddress)
	frame, _ := ParseFrame(downlink)
	if !frame.IsDownlinkFrame() {
		t.Error("reset link should be downlink")
	}

	// 上行确认帧 (FC=0, PRM=0) - 使用正确的校验和
	// C=0x00, A=0xFFFF -> CS = 0x00 + 0xFF + 0xFF = 0x1FE -> 0xFE
	upACK := []byte{0x10, 0x00, 0xFF, 0xFF, 0xFE, 0x16}
	frame2, _ := ParseFrame(upACK)
	if !frame2.IsUplinkFrame() {
		t.Error("FC=0 uplink should be uplink")
	}

	// 单字节确认
	frame3, _ := ParseFrame([]byte{0xE5})
	if frame3.IsDownlinkFrame() || frame3.IsUplinkFrame() {
		t.Error("single ACK should be neither")
	}
}

func TestBuildRequestFrames(t *testing.T) {
	// 请求1级数据 FC=10, FCV=1
	frame1 := BuildRequestLevel1Data(DefaultLinkAddress, false)
	if frame1[1] != 0x5A { // PRM=1, FCV=1, FC=10
		t.Errorf("RequestLevel1Data control: want 0x5A, got 0x%02X", frame1[1])
	}

	// 请求2级数据 FC=11, FCV=1
	frame2 := BuildRequestLevel2Data(DefaultLinkAddress, true) // FCB=1
	if frame2[1] != 0x7B { // PRM=1, FCB=1, FCV=1, FC=11
		t.Errorf("RequestLevel2Data control: want 0x7B, got 0x%02X", frame2[1])
	}

	// 请求链路状态 FC=9, FCV=0
	frame3 := BuildRequestLinkStatus(DefaultLinkAddress)
	if frame3[1] != 0x49 { // PRM=1, FC=9
		t.Errorf("RequestLinkStatus control: want 0x49, got 0x%02X", frame3[1])
	}
}

func TestSFrame(t *testing.T) {
	// S帧确认
	frame := BuildSFrame(DefaultLinkAddress, true) // 接收序号=1
	if frame[1] != 0x03 { // 0x01 | (1<<1) = 0x03
		t.Errorf("SFrame control: want 0x03, got 0x%02X", frame[1])
	}

	frame2 := BuildSFrame(DefaultLinkAddress, false)
	if frame2[1] != 0x01 {
		t.Errorf("SFrame control(recvSeq=0): want 0x01, got 0x%02X", frame2[1])
	}
}

func TestFrameRoundtrip(t *testing.T) {
	testFrames := [][]byte{
		BuildResetLink(DefaultLinkAddress),
		BuildStartDataTransfer(DefaultLinkAddress),
		BuildRequestLinkStatus(DefaultLinkAddress),
		BuildRequestLevel1Data(DefaultLinkAddress, false),
		BuildRequestLevel2Data(DefaultLinkAddress, true),
		BuildSendConfirmData(DefaultLinkAddress, false, []byte{0x01, 0x02, 0x03}),
		BuildSFrame(DefaultLinkAddress, true),
		BuildSingleACK(),
	}

	for i, original := range testFrames {
		parsed, err := ParseFrame(original)
		if err != nil {
			t.Errorf("frame %d roundtrip parse failed: %v", i, err)
			continue
		}
		if parsed.Type == FrameTypeVariable && parsed.ASDU != nil {
			// 重新构建并对比
			rebuilt := BuildVariableFrame(parsed.Control, parsed.Address, parsed.ASDU)
			if len(rebuilt) != len(original) {
				t.Errorf("frame %d rebuild length mismatch: %d vs %d", i, len(rebuilt), len(original))
			}
		}
	}
}

// BenchmarkParseFrame 基准测试
func BenchmarkParseFrame(b *testing.B) {
	asdu := make([]byte, 200)
	for i := range asdu {
		asdu[i] = byte(i)
	}
	frame := BuildSendConfirmData(DefaultLinkAddress, false, asdu)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ParseFrame(frame)
	}
}

func BenchmarkBuildVariableFrame(b *testing.B) {
	asdu := make([]byte, 200)
	for i := range asdu {
		asdu[i] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BuildSendConfirmData(DefaultLinkAddress, false, asdu)
	}
}