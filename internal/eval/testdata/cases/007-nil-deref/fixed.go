package main

import "fmt"

type user struct{ name string }

func main() {
	u := &user{name: "alice"}
	fmt.Println(u.name)
}
