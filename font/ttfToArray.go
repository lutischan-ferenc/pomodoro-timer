package main

import (
	"fmt"
	"os"
)

func main() {
	data, err := os.ReadFile("numbers.ttf")
	if err != nil {
		panic(err)
	}
	fmt.Println("var arialbdTTF = []byte{")
	for i, b := range data {
		if i%16 == 0 {
			fmt.Printf("\t")
		}
		fmt.Printf("0x%02x, ", b)
		if (i+1)%16 == 0 {
			fmt.Println()
		}
	}
	fmt.Println("\n}")
}