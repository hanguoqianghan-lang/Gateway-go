// internal/driver/modbus/merge_test.go - MergePoints 单元测试
package modbus

import (
	"sort"
	"testing"
)

// TestMergePoints_Empty 测试空输入
func TestMergePoints_Empty(t *testing.T) {
	blocks := MergePoints(nil, 4, 100)
	if blocks != nil {
		t.Errorf("期望 nil，实际得到 %d 个块", len(blocks))
	}

	blocks = MergePoints([]PointConfig{}, 4, 100)
	if blocks != nil {
		t.Errorf("期望 nil，实际得到 %d 个块", len(blocks))
	}
}

// TestMergePoints_Continuous 测试连续点位
func TestMergePoints_Continuous(t *testing.T) {
	points := []PointConfig{
		{Name: "p0", Address: 0, Type: HoldingRegister, DataType: Uint16},
		{Name: "p1", Address: 1, Type: HoldingRegister, DataType: Uint16},
		{Name: "p2", Address: 2, Type: HoldingRegister, DataType: Uint16},
		{Name: "p3", Address: 3, Type: HoldingRegister, DataType: Uint16},
	}

	blocks := MergePoints(points, 4, 100)

	if len(blocks) != 1 {
		t.Fatalf("期望 1 个块，实际得到 %d 个", len(blocks))
	}

	// 验证块属性
	if blocks[0].StartAddr != 0 {
		t.Errorf("期望起始地址 0，实际 %d", blocks[0].StartAddr)
	}
	if blocks[0].Count != 4 {
		t.Errorf("期望数量 4，实际 %d", blocks[0].Count)
	}
	if len(blocks[0].Points) != 4 {
		t.Errorf("期望 4 个测点，实际 %d", len(blocks[0].Points))
	}
}

// TestMergePoints_WithGap 测试有空洞的点位
func TestMergePoints_WithGap(t *testing.T) {
	tests := []struct {
		name       string
		points     []PointConfig
		maxGap     uint16
		wantBlocks int
		wantCounts []uint16
	}{
		{
			name: "小间隙在阈值内应合并",
			points: []PointConfig{
				{Name: "p0", Address: 0, Type: HoldingRegister, DataType: Uint16},
				{Name: "p1", Address: 5, Type: HoldingRegister, DataType: Uint16}, // gap=4
			},
			maxGap:     4,
			wantBlocks: 1,
			wantCounts: []uint16{6}, // 0~5
		},
		{
			name: "大间隙超出阈值应拆分",
			points: []PointConfig{
				{Name: "p0", Address: 0, Type: HoldingRegister, DataType: Uint16},
				{Name: "p1", Address: 10, Type: HoldingRegister, DataType: Uint16}, // gap=9
			},
			maxGap:     4,
			wantBlocks: 2,
			wantCounts: []uint16{1, 1},
		},
		{
			name: "多段连续点位",
			points: []PointConfig{
				{Name: "p0", Address: 0, Type: HoldingRegister, DataType: Uint16},
				{Name: "p1", Address: 1, Type: HoldingRegister, DataType: Uint16},
				{Name: "p2", Address: 10, Type: HoldingRegister, DataType: Uint16},
				{Name: "p3", Address: 11, Type: HoldingRegister, DataType: Uint16},
			},
			maxGap:     2,
			wantBlocks: 2,
			wantCounts: []uint16{2, 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks := MergePoints(tt.points, tt.maxGap, 100)

			if len(blocks) != tt.wantBlocks {
				t.Fatalf("期望 %d 个块，实际得到 %d 个", tt.wantBlocks, len(blocks))
			}

			for i, wantCount := range tt.wantCounts {
				if blocks[i].Count != wantCount {
					t.Errorf("块[%d]: 期望数量 %d，实际 %d", i, wantCount, blocks[i].Count)
				}
			}
		})
	}
}

// TestMergePoints_MaxRegsLimit 测试最大寄存器限制
func TestMergePoints_MaxRegsLimit(t *testing.T) {
	// 创建 150 个连续点位，超过 maxRegs=100
	points := make([]PointConfig, 150)
	for i := 0; i < 150; i++ {
		points[i] = PointConfig{
			Name:     "p",
			Address:  uint16(i),
			Type:     HoldingRegister,
			DataType: Uint16,
		}
	}

	blocks := MergePoints(points, 0, 100)

	// 应该拆分为 2 个块
	if len(blocks) != 2 {
		t.Fatalf("期望 2 个块，实际得到 %d 个", len(blocks))
	}

	// 第一个块：0~99，共 100 个寄存器
	if blocks[0].StartAddr != 0 || blocks[0].Count != 100 {
		t.Errorf("块[0]: 期望 StartAddr=0, Count=100，实际 StartAddr=%d, Count=%d",
			blocks[0].StartAddr, blocks[0].Count)
	}

	// 第二个块：100~149，共 50 个寄存器
	if blocks[1].StartAddr != 100 || blocks[1].Count != 50 {
		t.Errorf("块[1]: 期望 StartAddr=100, Count=50，实际 StartAddr=%d, Count=%d",
			blocks[1].StartAddr, blocks[1].Count)
	}
}

// TestMergePoints_MultiByteDataTypes 测试多字节数据类型
func TestMergePoints_MultiByteDataTypes(t *testing.T) {
	tests := []struct {
		name       string
		points     []PointConfig
		wantBlocks int
		wantCounts []uint16
	}{
		{
			name: "Float32 占用 2 个寄存器",
			points: []PointConfig{
				{Name: "f0", Address: 0, Type: HoldingRegister, DataType: Float32},
				{Name: "f1", Address: 2, Type: HoldingRegister, DataType: Float32},
			},
			wantBlocks: 1,
			wantCounts: []uint16{4}, // 0~3
		},
		{
			name: "Float64 占用 4 个寄存器",
			points: []PointConfig{
				{Name: "d0", Address: 0, Type: HoldingRegister, DataType: Float64},
				{Name: "d1", Address: 4, Type: HoldingRegister, DataType: Float64},
			},
			wantBlocks: 1,
			wantCounts: []uint16{8}, // 0~7
		},
		{
			name: "混合数据类型",
			points: []PointConfig{
				{Name: "u16", Address: 0, Type: HoldingRegister, DataType: Uint16},
				{Name: "f32", Address: 1, Type: HoldingRegister, DataType: Float32},
				{Name: "f64", Address: 3, Type: HoldingRegister, DataType: Float64},
			},
			wantBlocks: 1,
			wantCounts: []uint16{7}, // 0~6
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks := MergePoints(tt.points, 0, 100)

			if len(blocks) != tt.wantBlocks {
				t.Fatalf("期望 %d 个块，实际得到 %d 个", tt.wantBlocks, len(blocks))
			}

			for i, wantCount := range tt.wantCounts {
				if blocks[i].Count != wantCount {
					t.Errorf("块[%d]: 期望数量 %d，实际 %d", i, wantCount, blocks[i].Count)
				}
			}
		})
	}
}

// TestMergePoints_MultipleRegisterTypes 测试多种寄存器类型
func TestMergePoints_MultipleRegisterTypes(t *testing.T) {
	points := []PointConfig{
		{Name: "coil0", Address: 0, Type: Coil, DataType: Bool},
		{Name: "coil1", Address: 1, Type: Coil, DataType: Bool},
		{Name: "hr0", Address: 0, Type: HoldingRegister, DataType: Uint16},
		{Name: "hr1", Address: 1, Type: HoldingRegister, DataType: Uint16},
		{Name: "ir0", Address: 10, Type: InputRegister, DataType: Uint16},
	}

	blocks := MergePoints(points, 0, 100)

	// 应该按类型分为 3 个块
	if len(blocks) != 3 {
		t.Fatalf("期望 3 个块，实际得到 %d 个", len(blocks))
	}

	// 验证每个块的类型
	typeCount := make(map[RegisterType]int)
	for _, b := range blocks {
		typeCount[b.RegType]++
	}

	if typeCount[Coil] != 1 {
		t.Errorf("期望 1 个 Coil 块，实际 %d", typeCount[Coil])
	}
	if typeCount[HoldingRegister] != 1 {
		t.Errorf("期望 1 个 HoldingRegister 块，实际 %d", typeCount[HoldingRegister])
	}
	if typeCount[InputRegister] != 1 {
		t.Errorf("期望 1 个 InputRegister 块，实际 %d", typeCount[InputRegister])
	}
}

// TestMergePoints_UnorderedInput 测试无序输入
func TestMergePoints_UnorderedInput(t *testing.T) {
	points := []PointConfig{
		{Name: "p3", Address: 30, Type: HoldingRegister, DataType: Uint16},
		{Name: "p1", Address: 10, Type: HoldingRegister, DataType: Uint16},
		{Name: "p2", Address: 20, Type: HoldingRegister, DataType: Uint16},
		{Name: "p0", Address: 0, Type: HoldingRegister, DataType: Uint16},
	}

	blocks := MergePoints(points, 0, 100)

	// 应该合并为 4 个独立的块（间隙都大于 0）
	if len(blocks) != 4 {
		t.Fatalf("期望 4 个块，实际得到 %d 个", len(blocks))
	}

	// 验证块按地址升序排列
	for i := 1; i < len(blocks); i++ {
		if blocks[i].StartAddr <= blocks[i-1].StartAddr {
			t.Errorf("块未按地址升序排列: blocks[%d].StartAddr=%d <= blocks[%d].StartAddr=%d",
				i, blocks[i].StartAddr, i-1, blocks[i-1].StartAddr)
		}
	}
}

// TestMergePoints_PointsPreserved 测试测点是否完整保留
func TestMergePoints_PointsPreserved(t *testing.T) {
	points := []PointConfig{
		{Name: "p0", Address: 0, Type: HoldingRegister, DataType: Uint16},
		{Name: "p1", Address: 1, Type: HoldingRegister, DataType: Uint16},
		{Name: "p2", Address: 10, Type: HoldingRegister, DataType: Uint16},
		{Name: "p3", Address: 11, Type: HoldingRegister, DataType: Uint16},
	}

	blocks := MergePoints(points, 0, 100)

	// 统计所有块中的测点数量
	totalPoints := 0
	for _, b := range blocks {
		totalPoints += len(b.Points)
	}

	if totalPoints != len(points) {
		t.Errorf("测点丢失: 输入 %d 个，输出 %d 个", len(points), totalPoints)
	}

	// 验证每个测点都在某个块中
	pointNames := make(map[string]bool)
	for _, p := range points {
		pointNames[p.Name] = false
	}

	for _, b := range blocks {
		for _, p := range b.Points {
			if _, ok := pointNames[p.Name]; !ok {
				t.Errorf("未知测点: %s", p.Name)
			}
			pointNames[p.Name] = true
		}
	}

	for name, found := range pointNames {
		if !found {
			t.Errorf("测点未出现在任何块中: %s", name)
		}
	}
}

// TestMergePoints_EdgeCases 边界情况测试
func TestMergePoints_EdgeCases(t *testing.T) {
	t.Run("单个测点", func(t *testing.T) {
		points := []PointConfig{
			{Name: "single", Address: 100, Type: HoldingRegister, DataType: Uint16},
		}

		blocks := MergePoints(points, 4, 100)

		if len(blocks) != 1 {
			t.Fatalf("期望 1 个块，实际得到 %d 个", len(blocks))
		}
		if blocks[0].StartAddr != 100 || blocks[0].Count != 1 {
			t.Errorf("期望 StartAddr=100, Count=1，实际 StartAddr=%d, Count=%d",
				blocks[0].StartAddr, blocks[0].Count)
		}
	})

	t.Run("maxGap=0 不合并任何间隙", func(t *testing.T) {
		points := []PointConfig{
			{Name: "p0", Address: 0, Type: HoldingRegister, DataType: Uint16},
			{Name: "p1", Address: 2, Type: HoldingRegister, DataType: Uint16}, // gap=1
		}

		blocks := MergePoints(points, 0, 100)

		if len(blocks) != 2 {
			t.Fatalf("期望 2 个块，实际得到 %d 个", len(blocks))
		}
	})

	t.Run("maxRegs=1 每个测点独立", func(t *testing.T) {
		points := []PointConfig{
			{Name: "p0", Address: 0, Type: HoldingRegister, DataType: Uint16},
			{Name: "p1", Address: 1, Type: HoldingRegister, DataType: Uint16},
		}

		blocks := MergePoints(points, 0, 1)

		if len(blocks) != 2 {
			t.Fatalf("期望 2 个块，实际得到 %d 个", len(blocks))
		}
	})
}

// TestMergePoints_LargeDataset 大数据集测试
func TestMergePoints_LargeDataset(t *testing.T) {
	// 创建 1000 个测点，分布在 10 个段
	var points []PointConfig
	for seg := 0; seg < 10; seg++ {
		baseAddr := uint16(seg * 1000)
		for i := 0; i < 100; i++ {
			points = append(points, PointConfig{
				Name:     "p",
				Address:  baseAddr + uint16(i),
				Type:     HoldingRegister,
				DataType: Uint16,
			})
		}
	}

	blocks := MergePoints(points, 0, 100)

	// 每段 100 个连续测点，应该合并为 1 个块
	// 10 段，共 10 个块
	if len(blocks) != 10 {
		t.Fatalf("期望 10 个块，实际得到 %d 个", len(blocks))
	}

	// 验证每个块包含 100 个测点
	for i, b := range blocks {
		if len(b.Points) != 100 {
			t.Errorf("块[%d]: 期望 100 个测点，实际 %d", i, len(b.Points))
		}
	}
}

// TestMergePoints_SplitIfOversized 测试超大块拆分
func TestMergePoints_SplitIfOversized(t *testing.T) {
	// 创建一个跨越 200 个寄存器的 Float64 数组
	// Float64 占用 4 个寄存器，50 个 Float64 = 200 个寄存器
	points := make([]PointConfig, 50)
	for i := 0; i < 50; i++ {
		points[i] = PointConfig{
			Name:     "f64",
			Address:  uint16(i * 4),
			Type:     HoldingRegister,
			DataType: Float64,
		}
	}

	// maxRegs=100，应该拆分为 2 个块
	blocks := MergePoints(points, 0, 100)

	if len(blocks) != 2 {
		t.Fatalf("期望 2 个块，实际得到 %d 个", len(blocks))
	}

	// 验证所有测点都被保留
	totalPoints := 0
	for _, b := range blocks {
		totalPoints += len(b.Points)
	}
	if totalPoints != 50 {
		t.Errorf("测点丢失: 期望 50，实际 %d", totalPoints)
	}
}

// TestReadBlock_PointsSorted 验证块内测点按地址排序
func TestReadBlock_PointsSorted(t *testing.T) {
	points := []PointConfig{
		{Name: "p3", Address: 30, Type: HoldingRegister, DataType: Uint16},
		{Name: "p1", Address: 10, Type: HoldingRegister, DataType: Uint16},
		{Name: "p2", Address: 20, Type: HoldingRegister, DataType: Uint16},
		{Name: "p0", Address: 0, Type: HoldingRegister, DataType: Uint16},
	}

	blocks := MergePoints(points, 50, 100) // 大 gap 使其合并为一个块

	if len(blocks) != 1 {
		t.Fatalf("期望 1 个块，实际得到 %d 个", len(blocks))
	}

	// 验证测点按地址升序排列
	if !sort.SliceIsSorted(blocks[0].Points, func(i, j int) bool {
		return blocks[0].Points[i].Address < blocks[0].Points[j].Address
	}) {
		t.Error("块内测点未按地址升序排列")
	}
}
