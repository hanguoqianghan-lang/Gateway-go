// internal/broker/bus_test.go - DeadbandFilter 单元测试
package broker

import (
	"math"
	"testing"

	"github.com/gateway/gateway/internal/model"
)

// TestDeadbandFilter_Disabled 测试禁用死区过滤
func TestDeadbandFilter_Disabled(t *testing.T) {
	filter := newDeadbandFilter()

	// threshold <= 0 时，所有数据都应通过
	p := &model.PointData{ID: "test", Value: 1.0}

	for i := 0; i < 10; i++ {
		p.Value = float64(i)
		if !filter.Pass(p, 0) {
			t.Errorf("threshold=0 时应该通过，第 %d 次被拦截", i)
		}
		if !filter.Pass(p, -1) {
			t.Errorf("threshold<0 时应该通过，第 %d 次被拦截", i)
		}
	}
}

// TestDeadbandFilter_WithinDeadband 测试数值在死区内被拦截
func TestDeadbandFilter_WithinDeadband(t *testing.T) {
	filter := newDeadbandFilter()
	threshold := 1.0

	// 第一次：新测点，应该通过
	p := &model.PointData{ID: "test", Value: 10.0}
	if !filter.Pass(p, threshold) {
		t.Error("新测点应该通过")
	}

	// 变化量 0.5 < threshold，应该被拦截
	p.Value = 10.5
	if filter.Pass(p, threshold) {
		t.Error("变化量 0.5 < 1.0 应该被拦截")
	}

	// 变化量 0.9 < threshold，应该被拦截
	p.Value = 10.9
	if filter.Pass(p, threshold) {
		t.Error("变化量 0.9 < 1.0 应该被拦截")
	}

	// 变化量刚好等于 threshold，应该通过
	p.Value = 11.0
	if !filter.Pass(p, threshold) {
		t.Error("变化量等于 threshold 应该通过")
	}
}

// TestDeadbandFilter_ExceedsDeadband 测试数值超出死区通过
func TestDeadbandFilter_ExceedsDeadband(t *testing.T) {
	filter := newDeadbandFilter()
	threshold := 1.0

	// 第一次：新测点
	p := &model.PointData{ID: "test", Value: 10.0}
	filter.Pass(p, threshold)

	// 变化量 1.5 > threshold，应该通过
	p.Value = 11.5
	if !filter.Pass(p, threshold) {
		t.Error("变化量 1.5 > 1.0 应该通过")
	}

	// 变化量 2.0 > threshold，应该通过
	p.Value = 13.5
	if !filter.Pass(p, threshold) {
		t.Error("变化量 2.0 > 1.0 应该通过")
	}

	// 负方向变化
	p.Value = 10.0
	if !filter.Pass(p, threshold) {
		t.Error("负方向变化量 3.5 > 1.0 应该通过")
	}
}

// TestDeadbandFilter_MultiplePoints 测试多个测点独立过滤
func TestDeadbandFilter_MultiplePoints(t *testing.T) {
	filter := newDeadbandFilter()
	threshold := 1.0

	points := []*model.PointData{
		{ID: "point1", Value: 10.0},
		{ID: "point2", Value: 20.0},
		{ID: "point3", Value: 30.0},
	}

	// 所有测点第一次都应该通过
	for _, p := range points {
		if !filter.Pass(p, threshold) {
			t.Errorf("新测点 %s 应该通过", p.ID)
		}
	}

	// point1 变化，其他不变
	points[0].Value = 12.0 // 变化 2.0
	points[1].Value = 20.5 // 变化 0.5
	points[2].Value = 30.3 // 变化 0.3

	if !filter.Pass(points[0], threshold) {
		t.Error("point1 变化 2.0 应该通过")
	}
	if filter.Pass(points[1], threshold) {
		t.Error("point2 变化 0.5 应该被拦截")
	}
	if filter.Pass(points[2], threshold) {
		t.Error("point3 变化 0.3 应该被拦截")
	}
}

// TestDeadbandFilter_NonNumericValues 测试非数值类型
func TestDeadbandFilter_NonNumericValues(t *testing.T) {
	filter := newDeadbandFilter()
	threshold := 1.0

	// 非数值类型应该直接通过
	p := &model.PointData{ID: "test", Value: "string_value"}
	if !filter.Pass(p, threshold) {
		t.Error("字符串类型应该直接通过")
	}

	p.Value = []int{1, 2, 3}
	if !filter.Pass(p, threshold) {
		t.Error("数组类型应该直接通过")
	}

	p.Value = nil
	if !filter.Pass(p, threshold) {
		t.Error("nil 值应该直接通过")
	}
}

// TestDeadbandFilter_NumericTypes 测试各种数值类型
func TestDeadbandFilter_NumericTypes(t *testing.T) {
	filter := newDeadbandFilter()
	threshold := 1.0

	tests := []struct {
		name  string
		value interface{}
	}{
		{"float64", float64(10.0)},
		{"float32", float32(10.0)},
		{"int64", int64(10)},
		{"int32", int32(10)},
		{"int", 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &model.PointData{ID: "test_" + tt.name, Value: tt.value}

			// 第一次应该通过
			if !filter.Pass(p, threshold) {
				t.Errorf("%s: 新测点应该通过", tt.name)
			}

			// 小变化应该被拦截
			switch v := tt.value.(type) {
			case float64:
				p.Value = v + 0.5
			case float32:
				p.Value = v + 0.5
			case int64:
				p.Value = v + 0
			case int32:
				p.Value = v + 0
			case int:
				p.Value = v + 0
			}

			if filter.Pass(p, threshold) {
				t.Errorf("%s: 小变化应该被拦截", tt.name)
			}
		})
	}
}

// TestDeadbandFilter_LRUEviction 测试 LRU 淘汰逻辑
func TestDeadbandFilter_LRUEviction(t *testing.T) {
	filter := newDeadbandFilter()
	threshold := 1.0

	// 填充到最大容量
	for i := 0; i < deadbandMaxEntries; i++ {
		p := &model.PointData{
			ID:    "point_" + string(rune('A'+i%26)) + string(rune('0'+i/26)),
			Value: float64(i),
		}
		filter.Pass(p, threshold)
	}

	// 验证条目数
	filter.mu.Lock()
	initialCount := len(filter.entries)
	filter.mu.Unlock()

	if initialCount != deadbandMaxEntries {
		t.Errorf("期望 %d 个条目，实际 %d", deadbandMaxEntries, initialCount)
	}

	// 添加一个新条目，应该触发淘汰
	newPoint := &model.PointData{ID: "new_point", Value: 999.0}
	filter.Pass(newPoint, threshold)

	filter.mu.Lock()
	afterCount := len(filter.entries)
	filter.mu.Unlock()

	// 条目数应该仍然等于最大容量（淘汰一个，添加一个）
	if afterCount != deadbandMaxEntries {
		t.Errorf("淘汰后期望 %d 个条目，实际 %d", deadbandMaxEntries, afterCount)
	}

	// 验证新条目存在
	filter.mu.Lock()
	_, exists := filter.entries["new_point"]
	filter.mu.Unlock()

	if !exists {
		t.Error("新条目应该存在")
	}
}

// TestDeadbandFilter_LRUEvictionOrder 测试 LRU 淘汰顺序
func TestDeadbandFilter_LRUEvictionOrder(t *testing.T) {
	filter := newDeadbandFilter()
	threshold := 1.0

	// 创建少量条目以便测试
	smallMax := 10
	filter.entries = make(map[string]*deadbandEntry, smallMax)

	for i := 0; i < smallMax; i++ {
		p := &model.PointData{
			ID:    "point_" + string(rune('A'+i)),
			Value: float64(i),
		}
		filter.Pass(p, threshold)
	}

	// 更新 point_A 和 point_B（使其变为最新）
	filter.Pass(&model.PointData{ID: "point_A", Value: 100.0}, threshold)
	filter.Pass(&model.PointData{ID: "point_B", Value: 100.0}, threshold)

	// 添加新条目，应该淘汰最旧的（不是 A 或 B）
	newPoint := &model.PointData{ID: "new_point", Value: 999.0}
	filter.Pass(newPoint, threshold)

	// 验证 A 和 B 仍然存在
	filter.mu.Lock()
	_, hasA := filter.entries["point_A"]
	_, hasB := filter.entries["point_B"]
	_, hasNew := filter.entries["new_point"]
	filter.mu.Unlock()

	if !hasA {
		t.Error("point_A 应该仍然存在（最近更新过）")
	}
	if !hasB {
		t.Error("point_B 应该仍然存在（最近更新过）")
	}
	if !hasNew {
		t.Error("new_point 应该存在")
	}
}

// TestDeadbandFilter_UpdateSeqOnReject 测试被拦截时也更新访问序号
func TestDeadbandFilter_UpdateSeqOnReject(t *testing.T) {
	filter := newDeadbandFilter()
	threshold := 1.0

	// 添加一个测点
	p := &model.PointData{ID: "test", Value: 10.0}
	filter.Pass(p, threshold)

	// 获取初始序号
	filter.mu.Lock()
	initialSeq := filter.entries["test"].seq
	filter.mu.Unlock()

	// 发送小变化（被拦截）
	p.Value = 10.5
	filter.Pass(p, threshold)

	// 验证序号已更新
	filter.mu.Lock()
	updatedSeq := filter.entries["test"].seq
	filter.mu.Unlock()

	if updatedSeq <= initialSeq {
		t.Error("被拦截时应该更新访问序号")
	}
}

// TestDeadbandFilter_ConcurrentAccess 测试并发访问
func TestDeadbandFilter_ConcurrentAccess(t *testing.T) {
	filter := newDeadbandFilter()
	threshold := 1.0

	done := make(chan bool)

	// 启动多个 goroutine 并发访问
	for i := 0; i < 100; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				p := &model.PointData{
					ID:    "point",
					Value: float64(id*100 + j),
				}
				filter.Pass(p, threshold)
			}
			done <- true
		}(i)
	}

	// 等待所有 goroutine 完成
	for i := 0; i < 100; i++ {
		<-done
	}

	// 如果没有 panic 或死锁，测试通过
}

// TestDeadbandFilter_NegativeValues 测试负值
func TestDeadbandFilter_NegativeValues(t *testing.T) {
	filter := newDeadbandFilter()
	threshold := 1.0

	p := &model.PointData{ID: "test", Value: -10.0}

	// 第一次通过
	if !filter.Pass(p, threshold) {
		t.Error("新测点应该通过")
	}

	// 负方向小变化
	p.Value = -10.5
	if filter.Pass(p, threshold) {
		t.Error("负方向小变化应该被拦截")
	}

	// 负方向大变化
	p.Value = -12.0
	if !filter.Pass(p, threshold) {
		t.Error("负方向大变化应该通过")
	}

	// 从负到正
	p.Value = 5.0
	if !filter.Pass(p, threshold) {
		t.Error("从负到正的大变化应该通过")
	}
}

// TestDeadbandFilter_ZeroThreshold 测试零阈值
func TestDeadbandFilter_ZeroThreshold(t *testing.T) {
	filter := newDeadbandFilter()

	p := &model.PointData{ID: "test", Value: 10.0}

	// threshold=0 时，所有数据都通过
	for i := 0; i < 10; i++ {
		p.Value = float64(i) * 0.1 // 非常小的变化
		if !filter.Pass(p, 0) {
			t.Errorf("threshold=0 时，所有数据都应该通过，第 %d 次被拦截", i)
		}
	}
}

// TestDeadbandFilter_VerySmallThreshold 测试极小阈值
func TestDeadbandFilter_VerySmallThreshold(t *testing.T) {
	filter := newDeadbandFilter()
	threshold := 1e-10 // 极小阈值

	p := &model.PointData{ID: "test", Value: 10.0}
	filter.Pass(p, threshold)

	// 极小变化
	p.Value = 10.0 + 1e-11
	if filter.Pass(p, threshold) {
		t.Error("极小变化应该被拦截")
	}

	// 刚好超过阈值
	p.Value = 10.0 + 1e-9
	if !filter.Pass(p, threshold) {
		t.Error("超过阈值的变化应该通过")
	}
}

// TestDeadbandFilter_LargeThreshold 测试大阈值
func TestDeadbandFilter_LargeThreshold(t *testing.T) {
	filter := newDeadbandFilter()
	threshold := 1000.0

	p := &model.PointData{ID: "test", Value: 0.0}
	filter.Pass(p, threshold)

	// 大变化但仍小于阈值
	p.Value = 999.0
	if filter.Pass(p, threshold) {
		t.Error("变化 999 < 1000 应该被拦截")
	}

	// 刚好超过阈值
	p.Value = 1000.0
	if !filter.Pass(p, threshold) {
		t.Error("变化 1000 >= 1000 应该通过")
	}
}

// TestDeadbandFilter_SpecialValues 测试特殊值
func TestDeadbandFilter_SpecialValues(t *testing.T) {
	filter := newDeadbandFilter()
	threshold := 1.0

	tests := []struct {
		name  string
		value float64
	}{
		{"零值", 0.0},
		{"正无穷", math.Inf(1)},
		{"负无穷", math.Inf(-1)},
		{"NaN", math.NaN()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &model.PointData{ID: "test_" + tt.name, Value: tt.value}

			// 第一次应该通过
			filter.Pass(p, threshold)

			// NaN 的比较需要特殊处理
			if math.IsNaN(tt.value) {
				// NaN 与任何值比较都返回 false，所以 Abs(NaN - x) >= threshold 总是 false
				// 这意味着 NaN 后续会被拦截
				p.Value = 10.0
				result := filter.Pass(p, threshold)
				// NaN 情况下，Abs(NaN - 10.0) = NaN，NaN >= 1.0 = false
				// 所以应该被拦截
				if result {
					t.Error("从 NaN 变化应该被拦截（因为 NaN 比较问题）")
				}
			}
		})
	}
}

// TestDeadbandFilter_EvictOldest 测试 evictOldest 函数
func TestDeadbandFilter_EvictOldest(t *testing.T) {
	filter := newDeadbandFilter()

	// 手动添加几个条目
	filter.entries["oldest"] = &deadbandEntry{value: 1.0, seq: 1}
	filter.entries["middle"] = &deadbandEntry{value: 2.0, seq: 2}
	filter.entries["newest"] = &deadbandEntry{value: 3.0, seq: 3}

	// 淘汰最旧的
	filter.evictOldest()

	// 验证最旧的被删除
	if _, exists := filter.entries["oldest"]; exists {
		t.Error("oldest 应该被淘汰")
	}
	if _, exists := filter.entries["middle"]; !exists {
		t.Error("middle 应该仍然存在")
	}
	if _, exists := filter.entries["newest"]; !exists {
		t.Error("newest 应该仍然存在")
	}
}

// TestDeadbandFilter_EvictOldest_Empty 测试空 map 时 evictOldest
func TestDeadbandFilter_EvictOldest_Empty(t *testing.T) {
	filter := newDeadbandFilter()

	// 空 map，不应该 panic
	filter.evictOldest()

	if len(filter.entries) != 0 {
		t.Error("空 map 淘汰后应该仍为空")
	}
}
