package main

import "fmt"

func main() {
	n := 5
	if n == 0 && n == 1 { // BUG: 条件自相矛盾恒为假
		fmt.Println("never")
	}
}
