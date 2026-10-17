package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
)

func main() {
	path := os.Args[1]
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Println("read err:", err)
		return
	}
	fmt.Println("size:", len(raw))
	if zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw))); err != nil {
		fmt.Println("zip.NewReader err:", err)
	} else {
		fmt.Println("ok entries:", len(zr.File))
	}
	f, _ := os.Open(path)
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		fmt.Println("readAll err:", err)
		return
	}
	if zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err != nil {
		fmt.Println("second err:", err)
	} else {
		fmt.Println("second ok entries:", len(zr.File))
	}
}

