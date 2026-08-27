//go:build ignore

// Command generate creates deterministic, exact-size CSV fixtures for the
// LargeText end-to-end test.
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

type fixture struct {
	name  string
	label string
	size  int
}

func main() {
	fixtures := []fixture{
		{name: "large_text_1mb.csv", label: "1MB", size: 1_000_000},
		{name: "large_text_6mb.csv", label: "6MB", size: 6_000_000},
		{name: "large_text_10mb.csv", label: "10MB", size: 10_000_000},
	}
	for _, f := range fixtures {
		data := makeCSV(f.label, f.size)
		if err := os.WriteFile(filepath.Join("e2e", "testdata", f.name), data, 0o644); err != nil {
			panic(err)
		}
		fmt.Printf("generated %s (%d bytes)\n", f.name, len(data))
	}
}

func makeCSV(label string, size int) []byte {
	prefix := "id,name,email,balance,notes\n" +
		fmt.Sprintf("0,LARGE_TEXT_%s_HEAD,head-%s@example.com,0.00,BEGIN\n", label, label)
	repeated := "1,示例用户,user@example.com,123.45,fixture row\n"
	tail := fmt.Sprintf("999999,LARGE_TEXT_%s_TAIL,tail-%s@example.com,999.99,END\n", label, label)
	paddingPrefix := "998,padding,padding@example.com,0.00,\""
	paddingSuffix := "\"\n"
	minimumPadding := len(paddingPrefix) + len(paddingSuffix)

	var out bytes.Buffer
	out.Grow(size)
	out.WriteString(prefix)
	for size-out.Len()-len(tail)-len(repeated) >= minimumPadding {
		out.WriteString(repeated)
	}
	paddingBytes := size - out.Len() - len(tail)
	if paddingBytes < minimumPadding {
		panic("fixture is too small for a valid padding row")
	}
	out.WriteString(paddingPrefix)
	out.Write(bytes.Repeat([]byte{'x'}, paddingBytes-minimumPadding))
	out.WriteString(paddingSuffix)
	out.WriteString(tail)
	if out.Len() != size {
		panic(fmt.Sprintf("generated %d bytes, want %d", out.Len(), size))
	}
	return out.Bytes()
}
