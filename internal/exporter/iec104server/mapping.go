package iec104server

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/gateway/gateway/internal/model"
	"github.com/wendy512/go-iecp5/asdu"
)

// PointMapping 单个测点的 IEC104 映射配置
type PointMapping struct {
	// 点标识（与南向采集的 PointData.ID 对应）
	PointID string

	// IEC104 映射参数
	IOA    uint32      // 信息对象地址
	TypeID asdu.TypeID // 类型标识（如 M_ME_NC_1）
	Cot    uint8       // 传输原因（可选，默认使用全局配置）

	// 转换参数
	Scale  float64 // 缩放因子
	Offset float64 // 偏移量
}

// MappingManager 映射管理器
type MappingManager struct {
	// pointID -> mapping（O(1) 查找）
	mappings map[string]*PointMapping

	// 数据缓存（用于总召响应）
	dataCache map[string]*model.PointData
	cacheMu   sync.RWMutex
}

// NewMappingManager 创建映射管理器
func NewMappingManager() *MappingManager {
	return &MappingManager{
		mappings:  make(map[string]*PointMapping),
		dataCache: make(map[string]*model.PointData),
	}
}

// LoadFromCSV 从 CSV 文件加载映射配置
// CSV 格式：point_id,ioa,type_id,cot,scale,offset
func (m *MappingManager) LoadFromCSV(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open point file failed: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comment = '#'
	reader.TrimLeadingSpace = true

	// 跳过标题行
	records, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read csv failed: %w", err)
	}

	if len(records) == 0 {
		return nil
	}

	// 解析数据行（跳过标题）
	for i, record := range records {
		if i == 0 {
			// 检查是否是标题行
			if strings.ToLower(record[0]) == "point_id" {
				continue
			}
		}

		if len(record) < 3 {
			continue // 跳过不完整的行
		}

		mapping := &PointMapping{
			PointID: record[0],
		}

		// 解析 IOA
		ioa, err := strconv.ParseUint(record[1], 10, 32)
		if err != nil {
			continue // 跳过解析失败的行
		}
		mapping.IOA = uint32(ioa)

		// 解析 TypeID
		mapping.TypeID = parseTypeID(record[2])

		// 解析可选字段
		if len(record) > 3 && record[3] != "" {
			cot, _ := strconv.ParseUint(record[3], 10, 8)
			mapping.Cot = uint8(cot)
		}

		if len(record) > 4 && record[4] != "" {
			mapping.Scale, _ = strconv.ParseFloat(record[4], 64)
		} else {
			mapping.Scale = 1.0
		}

		if len(record) > 5 && record[5] != "" {
			mapping.Offset, _ = strconv.ParseFloat(record[5], 64)
		}

		m.mappings[mapping.PointID] = mapping
	}

	return nil
}

// parseTypeID 解析类型标识字符串
func parseTypeID(s string) asdu.TypeID {
	switch strings.ToUpper(s) {
	case "M_SP_NA_1":
		return asdu.M_SP_NA_1
	case "M_SP_TB_1":
		return asdu.M_SP_TB_1
	case "M_DP_NA_1":
		return asdu.M_DP_NA_1
	case "M_DP_TB_1":
		return asdu.M_DP_TB_1
	case "M_ST_NA_1":
		return asdu.M_ST_NA_1
	case "M_ST_TB_1":
		return asdu.M_ST_TB_1
	case "M_BO_NA_1":
		return asdu.M_BO_NA_1
	case "M_BO_TB_1":
		return asdu.M_BO_TB_1
	case "M_ME_NA_1":
		return asdu.M_ME_NA_1
	case "M_ME_NB_1":
		return asdu.M_ME_NB_1
	case "M_ME_NC_1":
		return asdu.M_ME_NC_1
	case "M_ME_ND_1":
		return asdu.M_ME_ND_1
	case "M_ME_TD_1":
		return asdu.M_ME_TD_1
	case "M_ME_TE_1":
		return asdu.M_ME_TE_1
	case "M_ME_TF_1":
		return asdu.M_ME_TF_1
	default:
		return asdu.M_ME_NC_1 // 默认浮点遥测
	}
}

// GetMapping 根据点 ID 获取映射配置
func (m *MappingManager) GetMapping(pointID string) (*PointMapping, bool) {
	mapping, ok := m.mappings[pointID]
	return mapping, ok
}

// UpdateCache 更新数据缓存（用于总召响应）
func (m *MappingManager) UpdateCache(data *model.PointData) {
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	// 复制数据到缓存
	cached := model.GetPoint()
	data.CopyTo(cached)
	m.dataCache[data.ID] = cached
}

// GetAllCachedData 获取所有缓存数据（用于总召响应）
func (m *MappingManager) GetAllCachedData() []*model.PointData {
	m.cacheMu.RLock()
	defer m.cacheMu.RUnlock()

	result := make([]*model.PointData, 0, len(m.dataCache))
	for _, data := range m.dataCache {
		result = append(result, data)
	}
	return result
}

// GetMappingCount 获取映射数量
func (m *MappingManager) GetMappingCount() int {
	return len(m.mappings)
}
