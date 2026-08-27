package main

import "fmt"

// Buffer 是有读偏移的字节缓冲。
type Buffer struct {
	buf []byte
	off int
}

// Peek 返回未读部分前 n 字节，不足则返回剩余并报错。
func (b *Buffer) Peek(n int) ([]byte, error) {
	if len(b.buf)-b.off < n {
		return b.buf[b.off:], fmt.Errorf("EOF")
	}
	return b.buf[b.off:n], nil
}

func main() {
	b := &Buffer{buf: []byte("helloworld"), off: 4}
	p, _ := b.Peek(3)
	fmt.Println(string(p))
}
