package main

import "fmt"

type user struct{ name string }

func main() {
	var u *user
	fmt.Println(u.name) // BUG: nil 指针解引用会 panic
}
