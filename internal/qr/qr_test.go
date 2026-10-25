package qr

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The worked example in Thonky's QR tutorial: "HELLO WORLD" as 1-M.  Its
// sixteen data codewords and the ten Reed–Solomon codewords they produce.
func TestReedSolomon(t *testing.T) {
	data := []byte{32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17}
	want := []byte{196, 35, 39, 119, 235, 215, 231, 226, 93, 23}
	if got := rsRemainder(data, rsDivisor(10)); !bytes.Equal(got, want) {
		t.Errorf("EC codewords %v, want %v", got, want)
	}
}

// Format strings for level M from ISO/IEC 18004 table C.1, and version
// strings from table D.1.
func TestFormatAndVersionBits(t *testing.T) {
	format := []string{
		"101010000010010", "101000100100101", "101111001111100", "101101101001011",
		"100010111111001", "100000011001110", "100111110010111", "100101010100000",
	}
	for m, w := range format {
		if got := strconv.FormatInt(int64(formatBits(m)), 2); leftPad(got, 15) != w {
			t.Errorf("mask %d: format %s, want %s", m, leftPad(got, 15), w)
		}
	}
	version := map[int]string{
		7: "000111110010010100", 8: "001000010110111100", 9: "001001101010011001", 10: "001010010011010011",
	}
	for v, w := range version {
		if got := leftPad(strconv.FormatInt(int64(versionBits(v)), 2), 18); got != w {
			t.Errorf("version %d: %s, want %s", v, got, w)
		}
	}
}

func leftPad(s string, n int) string { return strings.Repeat("0", n-len(s)) + s }

func TestCapacity(t *testing.T) {
	want := []int{0, 14, 26, 42, 62, 84, 106, 122, 152, 180, 213}
	for v := 1; v <= 10; v++ {
		if capacity(v) != want[v] {
			t.Errorf("version %d holds %d bytes, want %d", v, capacity(v), want[v])
		}
		total := 0
		for _, d := range blocks[v].data {
			total += d + blocks[v].ec
		}
		n := 17 + 4*v
		// Every module that isn't a function pattern carries a bit, less
		// the remainder bits (7 for versions 2–6, else 0).
		c := &Code{Version: v, Size: n, mod: make([]bool, n*n), fn: make([]bool, n*n)}
		c.functions()
		free := 0
		for _, f := range c.fn {
			if !f {
				free++
			}
		}
		if free/8 != total {
			t.Errorf("version %d: %d codewords in the table, room for %d", v, total, free/8)
		}
	}
	if _, err := Encode(make([]byte, MaxBytes+1)); err == nil {
		t.Error("encoded more than version 10 holds")
	}
}

// testdata/python-qrcode.txt holds whole symbols from python-qrcode 8, with
// the mask forced, for versions 1, 2, 5, 6, 7, 8, 9 and 10.  Matching them
// module for module covers the bit stream, the block interleave, placement,
// alignment, version and format information, and every mask.
func TestKnownSymbols(t *testing.T) {
	f, err := os.Open("testdata/python-qrcode.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		h := strings.Fields(sc.Text())
		if len(h) != 4 || h[0] != "v" {
			continue
		}
		v, _ := strconv.Atoi(h[1])
		mask, _ := strconv.Atoi(h[2])
		var data []byte
		if h[3] != "-" {
			data, _ = hex.DecodeString(h[3])
		}
		var rows []string
		for sc.Scan() && sc.Text() != "" {
			rows = append(rows, sc.Text())
		}
		c, err := encode(data, mask)
		if err != nil {
			t.Fatal(err)
		}
		if c.Version != v {
			t.Errorf("%d bytes: version %d, want %d", len(data), c.Version, v)
			continue
		}
		for y, row := range rows {
			for x, ch := range row {
				if c.Dark(x, y) != (ch == '1') {
					t.Errorf("version %d mask %d: module (%d,%d) differs", v, mask, x, y)
					goto next
				}
			}
		}
		n++
	next:
	}
	if n < 9 {
		t.Errorf("only %d symbols matched", n)
	}
}

func TestEncodePicksAMask(t *testing.T) {
	c, err := Encode([]byte("https://zawiper.example.org/drives/9VPAX9NA/2026-10-24T09-15-40Z-wipe"))
	if err != nil {
		t.Fatal(err)
	}
	forced, _ := encode([]byte("https://zawiper.example.org/drives/9VPAX9NA/2026-10-24T09-15-40Z-wipe"), c.Mask)
	if c.String() != forced.String() {
		t.Error("the chosen mask doesn't reproduce")
	}
	svg := c.SVG("qr")
	if !strings.HasPrefix(svg, `<svg class="qr"`) || !strings.Contains(svg, "viewBox=\"0 0 45 45\"") {
		t.Errorf("svg %.80s", svg)
	}
}
