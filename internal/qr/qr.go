// Package qr draws QR codes for the label on a wiped drive's bag.  It does
// exactly one thing: bytes in, at error correction level M, in versions 1 to
// 10.  That's up to 213 bytes, plenty for a record's address, and small
// enough to follow against ISO/IEC 18004 line by line.
package qr

import (
	"fmt"
	"strings"
)

// Code is a finished symbol.  Dark(x, y) is true for a dark module; x is
// the column and y the row, from the top left.
type Code struct {
	Version int
	Mask    int
	Size    int
	mod     []bool // Size×Size, row-major
	fn      []bool // function patterns: finders, timing, alignment, format
}

func (c *Code) Dark(x, y int) bool { return c.mod[y*c.Size+x] }

// Error correction level M, versions 1–10, from ISO/IEC 18004 table 9: EC
// codewords per block, and the data codewords in each block.  Short blocks
// come first.
var blocks = [11]struct {
	ec   int
	data []int
}{
	1:  {10, []int{16}},
	2:  {16, []int{28}},
	3:  {26, []int{44}},
	4:  {18, []int{32, 32}},
	5:  {24, []int{43, 43}},
	6:  {16, []int{27, 27, 27, 27}},
	7:  {18, []int{31, 31, 31, 31}},
	8:  {22, []int{38, 38, 39, 39}},
	9:  {22, []int{36, 36, 36, 37, 37}},
	10: {26, []int{43, 43, 43, 43, 44}},
}

// Alignment pattern centres, table E.1.
var align = [11][]int{
	2: {6, 18}, 3: {6, 22}, 4: {6, 26}, 5: {6, 30}, 6: {6, 34},
	7: {6, 22, 38}, 8: {6, 24, 42}, 9: {6, 26, 46}, 10: {6, 28, 50},
}

// MaxBytes is the most Encode takes.
const MaxBytes = 213

func dataCodewords(v int) int {
	n := 0
	for _, d := range blocks[v].data {
		n += d
	}
	return n
}

// capacity is how many bytes fit in version v: the mode indicator takes 4
// bits and the length 8 (16 from version 10).
func capacity(v int) int {
	return (dataCodewords(v)*8 - 4 - countBits(v)) / 8
}

func countBits(v int) int {
	if v >= 10 {
		return 16
	}
	return 8
}

// Encode makes the smallest symbol that holds b, choosing the mask the
// standard's penalty rules like best.
func Encode(b []byte) (*Code, error) {
	return encode(b, -1)
}

// encode with mask -1 picks the best mask; 0–7 forces one, for tests.
func encode(b []byte, mask int) (*Code, error) {
	v := 1
	for v <= 10 && capacity(v) < len(b) {
		v++
	}
	if v > 10 {
		return nil, fmt.Errorf("%d bytes won't fit; a version 10 QR code at level M holds %d", len(b), MaxBytes)
	}
	cw := interleave(v, codewords(v, b))

	c := &Code{Version: v, Size: 17 + 4*v}
	c.mod = make([]bool, c.Size*c.Size)
	c.fn = make([]bool, c.Size*c.Size)
	c.functions()
	c.place(cw)
	if mask < 0 {
		best := -1
		for m := 0; m < 8; m++ {
			c.applyMask(m)
			c.format(m)
			if p := c.penalty(); best < 0 || p < best {
				best, mask = p, m
			}
			c.applyMask(m) // XOR again undoes it
		}
	}
	c.Mask = mask
	c.applyMask(mask)
	c.format(mask)
	return c, nil
}

// codewords is the data bit stream for byte mode, padded out to the
// version's data capacity.
func codewords(v int, b []byte) []byte {
	var bits bitbuf
	bits.put(0b0100, 4) // byte mode
	bits.put(len(b), countBits(v))
	for _, x := range b {
		bits.put(int(x), 8)
	}
	total := dataCodewords(v) * 8
	bits.put(0, min(4, total-bits.n)) // terminator
	bits.put(0, (8-bits.n%8)%8)
	out := bits.b
	for i := 0; len(out) < dataCodewords(v); i++ {
		out = append(out, []byte{0xEC, 0x11}[i%2])
	}
	return out
}

type bitbuf struct {
	b []byte
	n int
}

func (w *bitbuf) put(v, bits int) {
	for i := bits - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		if v>>i&1 == 1 {
			w.b[w.n/8] |= 0x80 >> (w.n % 8)
		}
		w.n++
	}
}

// interleave splits data into blocks, adds each block's Reed–Solomon
// codewords, and deals them out a column at a time.
func interleave(v int, data []byte) []byte {
	bl := blocks[v]
	div := rsDivisor(bl.ec)
	var ds, es [][]byte
	for _, n := range bl.data {
		ds = append(ds, data[:n])
		es = append(es, rsRemainder(data[:n], div))
		data = data[n:]
	}
	var out []byte
	for i := 0; i < bl.data[len(bl.data)-1]; i++ {
		for _, d := range ds {
			if i < len(d) {
				out = append(out, d[i])
			}
		}
	}
	for i := 0; i < bl.ec; i++ {
		for _, e := range es {
			out = append(out, e[i])
		}
	}
	return out
}

// GF(256) with the QR polynomial x⁸+x⁴+x³+x²+1.
func gfMul(a, b byte) byte {
	var p byte
	for b > 0 {
		if b&1 == 1 {
			p ^= a
		}
		hi := a & 0x80
		a <<= 1
		if hi != 0 {
			a ^= 0x1D
		}
		b >>= 1
	}
	return p
}

// rsDivisor is the generator polynomial (x−α⁰)(x−α¹)…(x−α^(n−1)), highest
// power first, with the leading 1 left off.
func rsDivisor(n int) []byte {
	g := make([]byte, n)
	g[n-1] = 1
	root := byte(1)
	for i := 0; i < n; i++ {
		for j := range g {
			g[j] = gfMul(g[j], root)
			if j+1 < n {
				g[j] ^= g[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return g
}

func rsRemainder(data, div []byte) []byte {
	r := make([]byte, len(div))
	for _, b := range data {
		f := b ^ r[0]
		copy(r, r[1:])
		r[len(r)-1] = 0
		for i, d := range div {
			r[i] ^= gfMul(d, f)
		}
	}
	return r
}

func (c *Code) set(x, y int, dark bool) {
	c.mod[y*c.Size+x] = dark
	c.fn[y*c.Size+x] = true
}

// functions draws everything that isn't data: the three finders with their
// separators, the timing lines, alignment patterns, the dark module, and
// version information from version 7.  Format bits are reserved here and
// drawn once the mask is chosen.
func (c *Code) functions() {
	n := c.Size
	for i := 0; i < n; i++ {
		c.set(6, i, i%2 == 0)
		c.set(i, 6, i%2 == 0)
	}
	for _, p := range [][2]int{{3, 3}, {n - 4, 3}, {3, n - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := p[0]+dx, p[1]+dy
				if x < 0 || y < 0 || x >= n || y >= n {
					continue
				}
				d := max(abs(dx), abs(dy))
				c.set(x, y, d != 2 && d != 4)
			}
		}
	}
	a := align[c.Version]
	for i, ax := range a {
		for j, ay := range a {
			if (i == 0 && j == 0) || (i == 0 && j == len(a)-1) || (i == len(a)-1 && j == 0) {
				continue // under a finder
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					c.set(ax+dx, ay+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	c.format(0) // reserve
	if c.Version >= 7 {
		bits := versionBits(c.Version)
		for i := 0; i < 18; i++ {
			dark := bits>>i&1 == 1
			a, b := n-11+i%3, i/3
			c.set(a, b, dark)
			c.set(b, a, dark)
		}
	}
}

// formatBits is level M's two bits (00) and the mask, BCH-coded and masked
// with 101010000010010 so it's never all light.
func formatBits(mask int) int {
	d := mask // level M is 00
	r := d
	for i := 0; i < 10; i++ {
		r = r<<1 ^ (r>>9)*0x537
	}
	return (d<<10 | r) ^ 0x5412
}

func versionBits(v int) int {
	r := v
	for i := 0; i < 12; i++ {
		r = r<<1 ^ (r>>11)*0x1F25
	}
	return v<<12 | r
}

// format draws both copies of the format information.
func (c *Code) format(mask int) {
	bits, n := formatBits(mask), c.Size
	bit := func(i int) bool { return bits>>i&1 == 1 }
	for i := 0; i <= 5; i++ {
		c.set(8, i, bit(i))
	}
	c.set(8, 7, bit(6))
	c.set(8, 8, bit(7))
	c.set(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		c.set(14-i, 8, bit(i))
	}
	for i := 0; i < 8; i++ {
		c.set(n-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		c.set(8, n-15+i, bit(i))
	}
	c.set(8, n-8, true) // the dark module
}

// place lays the codewords in the zigzag: two columns at a time from the
// bottom right, up then down, skipping the vertical timing line.
func (c *Code) place(cw []byte) {
	n, i := c.Size, 0
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < n; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = n - 1 - vert
				}
				if !c.fn[y*n+x] && i < len(cw)*8 {
					c.mod[y*n+x] = cw[i/8]>>(7-i%8)&1 == 1
					i++
				}
			}
		}
	}
}

func (c *Code) applyMask(m int) {
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			var flip bool
			switch m {
			case 0:
				flip = (x+y)%2 == 0
			case 1:
				flip = y%2 == 0
			case 2:
				flip = x%3 == 0
			case 3:
				flip = (x+y)%3 == 0
			case 4:
				flip = (x/3+y/2)%2 == 0
			case 5:
				flip = x*y%2+x*y%3 == 0
			case 6:
				flip = (x*y%2+x*y%3)%2 == 0
			case 7:
				flip = ((x+y)%2+x*y%3)%2 == 0
			}
			if flip && !c.fn[y*c.Size+x] {
				c.mod[y*c.Size+x] = !c.mod[y*c.Size+x]
			}
		}
	}
}

// penalty scores a masked symbol by the four rules of ISO/IEC 18004 §7.8.3:
// long runs, 2×2 blocks, finder look-alikes, and dark/light imbalance.
func (c *Code) penalty() int {
	n, p, dark := c.Size, 0, 0
	at := func(x, y int, rows bool) bool {
		if rows {
			return c.Dark(x, y)
		}
		return c.Dark(y, x)
	}
	for _, rows := range []bool{true, false} {
		for y := 0; y < n; y++ {
			run := 1
			for x := 1; x <= n; x++ {
				if x < n && at(x, y, rows) == at(x-1, y, rows) {
					run++
					continue
				}
				if run >= 5 {
					p += 3 + run - 5
				}
				run = 1
			}
			// 1:1:3:1:1 with four light modules on one side.
			for x := 0; x+11 <= n; x++ {
				var s int
				for k := 0; k < 11; k++ {
					s <<= 1
					if at(x+k, y, rows) {
						s |= 1
					}
				}
				if s == 0b10111010000 || s == 0b00001011101 {
					p += 40
				}
			}
		}
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			d := c.Dark(x, y)
			if d {
				dark++
			}
			if x+1 < n && y+1 < n && d == c.Dark(x+1, y) && d == c.Dark(x, y+1) && d == c.Dark(x+1, y+1) {
				p += 3
			}
		}
	}
	return p + abs(dark*20-n*n*10)/(n*n)*10
}

// SVG draws the code with the standard four-module quiet zone, one unit a
// module, for CSS to size.  The path is one subpath per row run of dark
// modules, so it stays small.
func (c *Code) SVG(class string) string {
	q, n := 4, c.Size
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="%s" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="QR code">`, class, n+2*q, n+2*q)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="#000" d="`, n+2*q, n+2*q)
	for y := 0; y < n; y++ {
		for x := 0; x < n; {
			if !c.Dark(x, y) {
				x++
				continue
			}
			w := 1
			for x+w < n && c.Dark(x+w, y) {
				w++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", x+q, y+q, w, w)
			x += w
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

// String draws the code as text, two characters a module, for tests and
// terminals.
func (c *Code) String() string {
	var b strings.Builder
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.Dark(x, y) {
				b.WriteString("##")
			} else {
				b.WriteString("  ")
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
