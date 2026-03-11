// internal/driver/modbus/merge.go - 寄存器合并读取算法
//
// 算法目标：将同类型（RegisterType）的寄存器地址按连续性合并，
// 减少 Modbus TCP 请求次数，降低网络 RTT 开销。
//
// 例：同设备有 [0,2,4,6]、[10,12] 两段，合并为：
//   - 一次读 0~6 (len=7)
//   - 一次读 10~12 (len=3)
//
// 而非 4+2=6 次单独请求。
//
// 约束：单次请求最大读取 125 个寄存器（Modbus PDU 限制）。
package modbus

import "sort"

// ReadBlock 合并后的一次批量读请求描述
type ReadBlock struct {
	// RegType 寄存器类型（决定使用哪个功能码）
	RegType RegisterType
	// StartAddr 该批次的起始地址
	StartAddr uint16
	// Count 读取的寄存器数量（线圈类型时为线圈数）
	Count uint16
	// Points 属于本批次的测点，按地址升序排列
	Points []PointConfig
}

// MergePoints 对给定的测点列表按 RegisterType 分组，再按地址排序后合并连续地址，
// 生成最少请求次数的 ReadBlock 列表。
//
// maxGap：允许的地址间隙（空洞）大小，在此范围内的间隙会被"吞掉"合并进同一批次，
// 以换取更少的请求次数。典型值 4（读取少量无用寄存器好过多发一次请求）。
// maxRegs：单次请求最大读取寄存器数，默认100，某些老旧PLC可能需要更小的值
func MergePoints(points []PointConfig, maxGap uint16, maxRegs uint16) []ReadBlock {
	if len(points) == 0 {
		return nil
	}

	// 按 RegisterType 分组
	groups := make(map[RegisterType][]PointConfig)
	for _, p := range points {
		groups[p.Type] = append(groups[p.Type], p)
	}

	var blocks []ReadBlock
	for regType, pts := range groups {
		// 按起始地址升序排序，保证合并算法正确
		sort.Slice(pts, func(i, j int) bool {
			return pts[i].Address < pts[j].Address
		})
		blocks = append(blocks, mergeGroup(regType, pts, maxGap, maxRegs)...)
	}
	return blocks
}

// mergeGroup 对同类型、已排序的测点列表执行合并。
func mergeGroup(regType RegisterType, pts []PointConfig, maxGap uint16, maxRegs uint16) []ReadBlock {
	var blocks []ReadBlock

	// 当前批次的起始地址和已包含的测点
	curStart := pts[0].Address
	curPoints := []PointConfig{pts[0]}
	curEnd := pts[0].Address + registerWidth(pts[0].DataType) - 1 // 当前批次已覆盖的最后地址

	flush := func() {
		count := curEnd - curStart + 1
		if count == 0 {
			return
		}
		blocks = append(blocks, ReadBlock{
			RegType:   regType,
			StartAddr: curStart,
			Count:     count,
			Points:    curPoints,
		})
	}

	for i := 1; i < len(pts); i++ {
		p := pts[i]
		pEnd := p.Address + registerWidth(p.DataType) - 1

		gap := uint16(0)
		if p.Address > curEnd+1 {
			gap = p.Address - curEnd - 1
		}

		// 判断是否可以合并：间隙在阈值内，且合并后不超配置上限
		newCount := pEnd - curStart + 1
		canMerge := gap <= maxGap && newCount <= maxRegs

		if canMerge {
			// 扩展当前批次
			curPoints = append(curPoints, p)
			if pEnd > curEnd {
				curEnd = pEnd
			}
		} else {
			// 超出合并条件：先输出当前批次，再以本点开启新批次
			// 但当前批次本身可能也超出限制，需要先拆分
			splitBlocks := splitIfOversized(regType, curStart, curEnd, curPoints, maxRegs)
			blocks = append(blocks, splitBlocks...)

			curStart = p.Address
			curEnd = pEnd
			curPoints = []PointConfig{p}
		}

		_ = flush // flush 只在循环末尾调用，此处仅声明以消除 unused 警告
	}

	// 最后一批
	blocks = append(blocks, splitIfOversized(regType, curStart, curEnd, curPoints, maxRegs)...)
	return blocks
}

// splitIfOversized 若一个合并批次超过 maxRegs，按最大长度切割为多个 ReadBlock。
func splitIfOversized(regType RegisterType, start, end uint16, pts []PointConfig, maxRegs uint16) []ReadBlock {
	total := end - start + 1
	if total <= maxRegs {
		return []ReadBlock{{
			RegType:   regType,
			StartAddr: start,
			Count:     total,
			Points:    pts,
		}}
	}

	// 需要拆分：按物理地址区间切割，再将测点重新分配到各子块
	var blocks []ReadBlock
	blockStart := start
	for blockStart <= end {
		blockEnd := blockStart + maxRegs - 1
		if blockEnd > end {
			blockEnd = end
		}

		// 收集落在 [blockStart, blockEnd] 区间内的测点
		var subPts []PointConfig
		for _, p := range pts {
			pEnd := p.Address + registerWidth(p.DataType) - 1
			if p.Address >= blockStart && pEnd <= blockEnd {
				subPts = append(subPts, p)
			}
		}

		if len(subPts) > 0 {
			blocks = append(blocks, ReadBlock{
				RegType:   regType,
				StartAddr: blockStart,
				Count:     blockEnd - blockStart + 1,
				Points:    subPts,
			})
		}
		blockStart = blockEnd + 1
	}
	return blocks
}
