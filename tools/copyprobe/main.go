package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: copyprobe <source> <destination>")
		os.Exit(2)
	}
	in, err := os.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer in.Close()
	out, err := os.Create(os.Args[2])
	if err != nil {
		panic(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		panic(err)
	}
	if err := out.Sync(); err != nil {
		panic(err)
	}
	if err := out.Close(); err != nil {
		panic(err)
	}
}
