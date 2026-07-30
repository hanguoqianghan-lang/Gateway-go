package gb26875

import (
	"encoding/hex"
	"testing"
)

func h2b(s string) []byte {
	c := ""
	for _, ch := range s {
		if ch != ' ' {
			c += string(ch)
		}
	}
	b, _ := hex.DecodeString(c)
	return b
}

// 案例1 控制命令
// 4040 0000 0101 011E16080C15 800D00000000 000000000000 0400 02 19010100 0E 2323
func TestParseFrame_Case1(t *testing.T) {
	raw := h2b("404000000101011E16080C15800D00000000000000000000040002190101000E2323")
	f, err := ParseFrame(raw)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	if f.SequenceNo != 0 {
		t.Errorf("SeqNo: want 0, got %d", f.SequenceNo)
	}
	if f.Version != 1 {
		t.Errorf("Version: want 1, got %d", f.Version)
	}
	if f.UserVer != 1 {
		t.Errorf("UserVer: want 1, got %d", f.UserVer)
	}
	if f.Cmd != CmdSendData {
		t.Errorf("Cmd: want 2, got %d", f.Cmd)
	}
	if f.ADULength != 4 {
		t.Errorf("ADULen: want 4, got %d", f.ADULength)
	}
	// 源地址 = 线网字节 {0x80,0x0D,0,0,0,0}（低字节在前/线网序）
	// StringAddr 按线网字节序输出 → "800D00000000"
	if StringAddr(f.SrcAddr) != "800D00000000" {
		t.Errorf("SrcAddr: want 800D00000000, got %s", StringAddr(f.SrcAddr))
	}
}

// 案例2 确认应答
// 4040 0000 0101 001E16080C15 000000000000 800D00000000 0000 03 EF 2323
func TestParseFrame_Case2(t *testing.T) {
	raw := h2b("404000000101001E16080C15000000000000800D00000000000003EF2323")
	f, err := ParseFrame(raw)
	if err != nil {
		t.Fatalf("ParseFrame: %v", err)
	}
	if f.Cmd != CmdConfirm {
		t.Errorf("Cmd: want 3, got %d", f.Cmd)
	}
	if f.ADULength != 0 {
		t.Errorf("ADULen: want 0, got %d", f.ADULength)
	}
	if f.CS != 0xEF {
		t.Errorf("CS: want 0xEF, got 0x%02X", f.CS)
	}
}

func TestParseFrame_Short(t *testing.T) {
	_, err := ParseFrame(h2b("4040"))
	if err == nil {
		t.Error("expected error")
	}
}

func TestParseFrame_BadStart(t *testing.T) {
	d := make([]byte, 40)
	d[0] = 0x41
	d[1] = 0x41
	d[38] = 0x23
	d[39] = 0x23
	_, err := ParseFrame(d)
	if err == nil {
		t.Error("expected error")
	}
}

func TestParseFrame_BadEnd(t *testing.T) {
	d := make([]byte, 40)
	d[0] = 0x40
	d[1] = 0x40
	d[38] = 0x24
	d[39] = 0x24
	_, err := ParseFrame(d)
	if err == nil {
		t.Error("expected error")
	}
}

func TestChecksum(t *testing.T) {
	// 案例1的控制单元 + ADU → CS=0x0E
	cu := h2b("00000101011E16080C15800D0000000000000000000004000219010100")
	cs := calculateChecksum(cu)
	if cs != 0x0E {
		t.Errorf("want 0xE, got 0x%02X", cs)
	}

	// 案例2的CU (no ADU) → CS=0xEF
	cu2 := h2b("00000101001E16080C15000000000000800D00000000000003")
	cs2 := calculateChecksum(cu2)
	if cs2 != 0xEF {
		t.Errorf("case2: want 0xEF, got 0x%02X", cs2)
	}
}

func TestBuildAckFrame2(t *testing.T) {
	// 案例2实际报文: 4040 0000 0001 0101
	tl := TimeLabel{0x01, 0x1E, 0x16, 0x08, 0x0C, 0x15}
	src := [6]byte{0x80, 0x0D, 0x00, 0x00, 0x00, 0x00}
	dst := [6]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

	frame := BuildAckFrame(0, 1, 1, tl, src, dst)

	if len(frame) < MinFrameLen {
		t.Fatalf("frame too short: %d", len(frame))
	}
	if frame[0] != FrameStart1 || frame[1] != FrameStart2 {
		t.Error("bad start marker")
	}
	if frame[len(frame)-2] != FrameEnd1 || frame[len(frame)-1] != FrameEnd2 {
		t.Error("bad end marker")
	}

	// Parse back and verify
	f, err := ParseFrame(frame)
	if err != nil {
		t.Fatalf("roundtrip parse failed: %v", err)
	}
	if f.Cmd != CmdConfirm {
		t.Errorf("parsed Cmd: want 3, got %d", f.Cmd)
	}
}

func TestBuildFrameRoundtrip(t *testing.T) {
	tl := TimeLabel{0x22, 0x11, 0x0D, 0x0A, 0x09, 0x15}
	src := [6]byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x00}
	dst := [6]byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	adu := []byte{0x02, 0x01}

	frame := BuildFrame(0x067D, 1, 1, tl, src, dst, CmdSendData, adu)
	f, err := ParseFrame(frame)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	if f.SequenceNo != 0x067D {
		t.Errorf("seq: want 0x067D, got 0x%04X", f.SequenceNo)
	}
	if f.Cmd != CmdSendData {
		t.Errorf("cmd: want 2, got %d", f.Cmd)
	}
	if f.ADULength != 2 {
		t.Errorf("adulen: want 2, got %d", f.ADULength)
	}
}

func TestBuildAllVariant(t *testing.T) {
	seq := uint16(1)
	v := uint8(1)
	u := uint8(1)
	tl := TimeLabel{0x01, 0x1E, 0x16, 0x08, 0x0C, 0x15}
	src := [6]byte{0x80, 0x0D, 0, 0, 0, 0}
	dst := [6]byte{0, 0, 0, 0, 0, 0}
	adu := []byte{0x19, 0x01, 0x01, 0x00}

	tests := []struct {
		name  string
		frame []byte
	}{
		{"Ack", BuildAckFrame(seq, v, u, tl, src, dst)},
		{"Deny", BuildDenyFrame(seq, v, u, tl, src, dst)},
		{"Request", BuildRequestFrame(seq, v, u, tl, src, dst, adu)},
		{"Control", BuildControlFrame(seq, v, u, tl, src, dst, adu)},
		{"Reply", BuildReplyFrame(seq, v, u, tl, src, dst, adu)},
		{"SendData_nilDU", BuildFrame(seq, v, u, tl, src, dst, CmdSendData, nil)},
	}

	for _, tt := range tests {
		if len(tt.frame) < 30 {
			t.Errorf("%s: too short (%d)", tt.name, len(tt.frame))
			continue
		}
		f, err := ParseFrame(tt.frame)
		if err != nil {
			t.Errorf("%s: parse err: %v", tt.name, err)
		} else if f.Cmd == 0 {
			t.Errorf("%s: Cmd=0 after parse", tt.name)
		}
	}
}

func TestFrameHelpers(t *testing.T) {
	tests := []struct {
		cmd     uint8
		upload  bool
		command bool
		ack     bool
	}{
		{CmdSendData, true, false, false},
		{CmdReply, true, false, false},
		{CmdControl, false, true, false},
		{CmdRequest, false, true, false},
		{CmdConfirm, false, false, true},
		{CmdDeny, false, false, true},
	}
	for _, tt := range tests {
		f := &Frame{Cmd: tt.cmd}
		if f.IsUpload() != tt.upload {
			t.Errorf("Cmd=%d IsUpload want %v", tt.cmd, tt.upload)
		}
		if f.IsCommand() != tt.command {
			t.Errorf("Cmd=%d IsCommand want %v", tt.cmd, tt.command)
		}
		if f.IsAck() != tt.ack {
			t.Errorf("Cmd=%d IsAck want %v", tt.cmd, tt.ack)
		}
	}
}

func TestTimeLabel(t *testing.T) {
	tl := FormatTimeLabel(21, 9, 10, 13, 17, 34)
	if tl.IsZero() {
		t.Error("should be non-zero")
	}
	// BCD bytes: sec=0x34, min=0x17, hour=0x13, day=0x10, month=0x09, year=0x15
	if tl[0] != 0x34 {
		t.Errorf("sec: want 0x34, got 0x%02X", tl[0])
	}
	if tl[1] != 0x17 {
		t.Errorf("min: want 0x17, got 0x%02X", tl[1])
	}
	if tl[3] != 0x10 {
		t.Errorf("day: want 0x10, got 0x%02X", tl[3])
	}
	if tl[4] != 0x09 {
		t.Errorf("month: want 0x09, got 0x%02X", tl[4])
	}
}

func TestAddrString(t *testing.T) {
	// 线网字节序存储：a=[0x80, 0x0D, 0,0,0,0]
	a := [6]byte{0x80, 0x0D, 0, 0, 0, 0}
	s := StringAddr(a)
	// 按线网字节序直接输出：80 0D 00 00 00 00
	if s != "800D00000000" {
		t.Errorf("got %s", s)
	}
}

func TestParseAddrString(t *testing.T) {
	a1, err1 := ParseAddrString("800D00000000")
	if err1 != nil {
		t.Fatal("ParseAddrString:", err1)
	}
	// "800D00000000" 为线网字节序字符串，按线网顺序存储 [0x80,0x0D,0,0,0,0]
	// StringAddr 按线网字节序输出 → 回到 "800D00000000"
	if StringAddr(a1) != "800D00000000" {
		t.Errorf("got %s", StringAddr(a1))
	}
	// With dashes
	a2, err2 := ParseAddrString("80-0D-00-00-00-00")
	if err2 != nil {
		t.Fatal("ParseAddrString(dashed):", err2)
	}
	if StringAddr(a2) != StringAddr(a1) {
		t.Error("dashed should match plain")
	}
}