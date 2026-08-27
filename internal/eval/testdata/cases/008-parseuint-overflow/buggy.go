package main

import "fmt"

// maxUint64 是 uint64 上限。
const maxUint64 = ^uint64(0)

// parseUint 解析十进制串，超出 bitSize 有效位时返回范围错误，
// 返回值应钳位在 [0, 1<<bitSize-1]。
func parseUint(s string, bitSize int) (uint64, error) {
	maxVal := uint64(1)<<uint(bitSize) - 1
	var n uint64
	for _, c := range s {
		d := uint64(c - '0')
		if n >= (maxUint64-d)/10 {
			n = maxUint64 // 算术溢出：应对齐 maxVal
			return n, fmt.Errorf("value out of range")
		}
		n = n*10 + d
	}
	if n > maxVal {
		n = maxUint64 // 超 bitSize：应对齐 maxVal
		return n, fmt.Errorf("value out of range")
	}
	return n, nil
}

func main() {
	v, err := parseUint("4294967296", 32) // 2^32，超出 32 位上限
	fmt.Println(v, err)
}
