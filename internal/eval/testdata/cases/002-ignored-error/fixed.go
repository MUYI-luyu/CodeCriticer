package main

import (
	"log"
	"os"
)

func main() {
	if err := os.Chdir("/tmp"); err != nil {
		log.Fatal(err)
	}
}
