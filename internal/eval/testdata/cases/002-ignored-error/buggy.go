package main

import "os"

func main() {
	os.Chdir("/tmp") // BUG: 忽略 Chdir 返回的错误
}
