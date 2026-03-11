// cmd/mbtest/main.go - Modbus TCP 连通性诊断工具
// 用法：go run ./cmd/mbtest/ -host 127.0.0.1 -port 502 -unit 1 -addr 100 -count 10
package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/simonvetter/modbus"
)

func main() {
	host  := flag.String("host",  "127.0.0.1", "Modbus Slave IP")
	port  := flag.Int("port",  502, "Modbus TCP 端口")
	unit  := flag.Int("unit",  1,   "Unit ID（Slave 地址）")
	addr  := flag.Int("addr",  100, "起始寄存器地址（0-based）")
	count := flag.Int("count", 10,  "读取寄存器数量")
	flag.Parse()

	url := fmt.Sprintf("tcp://%s:%d", *host, *port)
	fmt.Printf(">>> 连接 %s  Unit=%d  addr=%d  count=%d\n", url, *unit, *addr, *count)

	client, err := modbus.NewClient(&modbus.ClientConfiguration{
		URL:     url,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		fmt.Printf("[FAIL] 创建 client 失败: %v\n", err)
		return
	}

	if err = client.Open(); err != nil {
		fmt.Printf("[FAIL] 连接失败: %v\n", err)
		fmt.Println()
		fmt.Println("排查建议：")
		fmt.Println("  1. 执行  netstat -ano | findstr :502  确认模拟器是否在监听")
		fmt.Println("  2. 若输出为空，以管理员权限重启模拟器，或换用高位端口（如 5020）")
		fmt.Println("  3. 换端口后用  go run ./cmd/mbtest/ -port 5020  重试")
		return
	}
	defer client.Close()
	fmt.Println("[OK]  TCP 连接成功")

	client.SetUnitId(uint8(*unit))

	regs, err := client.ReadRegisters(uint16(*addr), uint16(*count), modbus.HOLDING_REGISTER)
	if err != nil {
		fmt.Printf("[FAIL] ReadRegisters 失败: %v\n", err)
		fmt.Println()
		fmt.Println("排查建议：")
		fmt.Println("  1. 检查模拟器的 Unit ID 是否与 -unit 参数一致（默认1）")
		fmt.Println("  2. 检查模拟器寄存器表是否包含地址 100~109（Function Code 03）")
		fmt.Println("  3. 尝试  go run ./cmd/mbtest/ -unit 0  或  -unit 255")
		return
	}

	fmt.Printf("[OK]  读取成功，%d 个寄存器：\n", len(regs))
	for i, v := range regs {
		signed := int16(v)
		fmt.Printf("  addr %3d : raw=0x%04X  uint16=%5d  int16=%6d\n",
			*addr+i, v, v, signed)
	}
}
