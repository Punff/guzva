// Package qr is a small QR Code encoder (ISO/IEC 18004) for the venue
// share card and the printable table poster: byte mode, error correction
// level M (~15 % damage tolerance — a poster on a café table gets wet),
// versions 1–10. Stdlib only (no new Go deps); output is SVG so it prints
// sharp at any size. The layout follows the standard construction, the same
// one Project Nayuki's reference encoder documents step by step.
package qr

import (
	"errors"
	"fmt"
	"strings"
)

// blocks[v] = data codewords per block for version v at level M; ecLen[v]
// is the EC codewords per block. Short blocks come first.
var (
	ecLen  = [11]int{0, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26}
	blocks = [11][]int{
		nil, {16}, {28}, {44}, {32, 32}, {43, 43}, {27, 27, 27, 27},
		{31, 31, 31, 31}, {38, 38, 39, 39}, {36, 36, 36, 37, 37},
		{43, 43, 43, 43, 44},
	}
	alignPos = [11][]int{
		nil, nil, {6, 18}, {6, 22}, {6, 26}, {6, 30}, {6, 34},
		{6, 22, 38}, {6, 24, 42}, {6, 26, 46}, {6, 28, 50},
	}
)

// ErrTooLong is returned when the text does not fit version 10-M.
var ErrTooLong = errors.New("qr: text too long")

// Code is an encoded symbol: Size×Size modules, true = dark.
type Code struct {
	Size    int
	Version int
	Mask    int
	mod     [][]bool
	fn      [][]bool // function modules (not data)
}

// Dark reports whether the module at column x, row y is dark.
func (c *Code) Dark(x, y int) bool { return c.mod[y][x] }

// Encode builds the smallest symbol (level M) holding text as bytes.
func Encode(text string) (*Code, error) {
	data := []byte(text)
	ver := 0
	for v := 1; v <= 10; v++ {
		capBits := 0
		for _, n := range blocks[v] {
			capBits += n * 8
		}
		lenBits := 8
		if v >= 10 {
			lenBits = 16
		}
		if 4+lenBits+len(data)*8 <= capBits {
			ver = v
			break
		}
	}
	if ver == 0 {
		return nil, ErrTooLong
	}

	// Bit stream: mode 0100 (byte), count, data, terminator, pad.
	var bits []bool
	put := func(v, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, v>>i&1 == 1)
		}
	}
	put(4, 4)
	if ver >= 10 {
		put(len(data), 16)
	} else {
		put(len(data), 8)
	}
	for _, b := range data {
		put(int(b), 8)
	}
	capBits := 0
	for _, n := range blocks[ver] {
		capBits += n * 8
	}
	put(0, min(4, capBits-len(bits)))
	for len(bits)%8 != 0 {
		bits = append(bits, false)
	}
	for pad := 0xEC; len(bits) < capBits; pad ^= 0xEC ^ 0x11 {
		put(pad, 8)
	}
	cw := make([]byte, len(bits)/8)
	for i, b := range bits {
		if b {
			cw[i>>3] |= 1 << (7 - i&7)
		}
	}

	// Split into blocks, add Reed-Solomon EC, interleave.
	div := rsDivisor(ecLen[ver])
	var dblk, eblk [][]byte
	off := 0
	for _, n := range blocks[ver] {
		d := cw[off : off+n]
		off += n
		dblk = append(dblk, d)
		eblk = append(eblk, rsRemainder(d, div))
	}
	var final []byte
	for i := 0; i < blocks[ver][len(blocks[ver])-1]; i++ {
		for _, d := range dblk {
			if i < len(d) {
				final = append(final, d[i])
			}
		}
	}
	for i := 0; i < ecLen[ver]; i++ {
		for _, e := range eblk {
			final = append(final, e[i])
		}
	}

	c := &Code{Size: 17 + 4*ver, Version: ver}
	c.mod = grid(c.Size)
	c.fn = grid(c.Size)
	c.drawFunction()
	c.drawData(final)

	best, bestPen := 0, -1
	for m := 0; m < 8; m++ {
		c.applyMask(m)
		c.drawFormat(m)
		if p := c.penalty(); bestPen < 0 || p < bestPen {
			best, bestPen = m, p
		}
		c.applyMask(m) // XOR again undoes it
	}
	c.Mask = best
	c.applyMask(best)
	c.drawFormat(best)
	return c, nil
}

func grid(n int) [][]bool {
	g := make([][]bool, n)
	for i := range g {
		g[i] = make([]bool, n)
	}
	return g
}

func (c *Code) set(x, y int, dark bool) {
	c.mod[y][x] = dark
	c.fn[y][x] = true
}

func (c *Code) drawFunction() {
	n := c.Size
	for i := 0; i < n; i++ { // timing patterns
		c.set(6, i, i%2 == 0)
		c.set(i, 6, i%2 == 0)
	}
	finder := func(cx, cy int) {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := cx+dx, cy+dy
				if x < 0 || y < 0 || x >= n || y >= n {
					continue
				}
				d := max(abs(dx), abs(dy))
				c.set(x, y, d != 2 && d != 4)
			}
		}
	}
	finder(3, 3)
	finder(n-4, 3)
	finder(3, n-4)
	pos := alignPos[c.Version]
	for i, ay := range pos {
		for j, ax := range pos {
			if (i == 0 && j == 0) || (i == 0 && j == len(pos)-1) || (i == len(pos)-1 && j == 0) {
				continue // under a finder
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					c.set(ax+dx, ay+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	c.drawFormat(0) // reserve; real bits drawn after masking
	if c.Version >= 7 {
		rem := c.Version
		for i := 0; i < 12; i++ {
			rem = rem<<1 ^ (rem>>11)*0x1F25
		}
		v := c.Version<<12 | rem
		for i := 0; i < 18; i++ {
			bit := v>>i&1 == 1
			a, b := n-11+i%3, i/3
			c.set(a, b, bit)
			c.set(b, a, bit)
		}
	}
}

// drawFormat writes the 15 format bits (level M = 00, mask) in both copies.
func (c *Code) drawFormat(mask int) {
	d := 0<<3 | mask
	rem := d
	for i := 0; i < 10; i++ {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	bits := (d<<10 | rem) ^ 0x5412
	bit := func(i int) bool { return bits>>i&1 == 1 }
	n := c.Size
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

// drawData places codewords in the two-column zigzag from the bottom right.
func (c *Code) drawData(data []byte) {
	n := c.Size
	i := 0
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
				if !c.fn[y][x] && i < len(data)*8 {
					c.mod[y][x] = data[i>>3]>>(7-i&7)&1 == 1
					i++
				}
			}
		}
	}
}

func (c *Code) applyMask(m int) {
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.fn[y][x] {
				continue
			}
			var inv bool
			switch m {
			case 0:
				inv = (x+y)%2 == 0
			case 1:
				inv = y%2 == 0
			case 2:
				inv = x%3 == 0
			case 3:
				inv = (x+y)%3 == 0
			case 4:
				inv = (x/3+y/2)%2 == 0
			case 5:
				inv = x*y%2+x*y%3 == 0
			case 6:
				inv = (x*y%2+x*y%3)%2 == 0
			case 7:
				inv = ((x+y)%2+x*y%3)%2 == 0
			}
			if inv {
				c.mod[y][x] = !c.mod[y][x]
			}
		}
	}
}

// penalty scores a masked symbol by the four standard rules; lower reads
// better. Any mask decodes — this only picks the most scanner-friendly.
func (c *Code) penalty() int {
	n, p := c.Size, 0
	line := func(get func(i int) bool) {
		run := 1
		for i := 1; i < n; i++ {
			if get(i) == get(i-1) {
				run++
				continue
			}
			if run >= 5 {
				p += run - 2
			}
			run = 1
		}
		if run >= 5 {
			p += run - 2
		}
		// 1:1:3:1:1 finder look-alikes with 4 light modules on one side.
		for i := 0; i+11 <= n; i++ {
			a := []bool{true, false, true, true, true, false, true, false, false, false, false}
			b := []bool{false, false, false, false, true, false, true, true, true, false, true}
			ma, mb := true, true
			for k := 0; k < 11; k++ {
				ma = ma && get(i+k) == a[k]
				mb = mb && get(i+k) == b[k]
			}
			if ma {
				p += 40
			}
			if mb {
				p += 40
			}
		}
	}
	dark := 0
	for y := 0; y < n; y++ {
		line(func(i int) bool { return c.mod[y][i] })
		line(func(i int) bool { return c.mod[i][y] })
		for x := 0; x < n; x++ {
			if c.mod[y][x] {
				dark++
			}
			if x+1 < n && y+1 < n {
				v := c.mod[y][x]
				if c.mod[y][x+1] == v && c.mod[y+1][x] == v && c.mod[y+1][x+1] == v {
					p += 3
				}
			}
		}
	}
	total := n * n
	k := (abs(dark*20-total*10) + total - 1) / total
	return p + max(k-1, 0)*10
}

// --- Reed-Solomon over GF(2^8), polynomial 0x11D -----------------------------

func gfMul(x, y byte) byte {
	var z byte
	for i := 7; i >= 0; i-- {
		hi := z >> 7
		z = z<<1 ^ hi*0x1D
		z ^= (y >> i & 1) * x
	}
	return z
}

func rsDivisor(degree int) []byte {
	r := make([]byte, degree)
	r[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := range r {
			r[j] = gfMul(r[j], root)
			if j+1 < len(r) {
				r[j] ^= r[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return r
}

func rsRemainder(data, div []byte) []byte {
	r := make([]byte, len(div))
	for _, b := range data {
		f := b ^ r[0]
		copy(r, r[1:])
		r[len(r)-1] = 0
		for i := range r {
			r[i] ^= gfMul(div[i], f)
		}
	}
	return r
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// SVG renders the code with the standard 4-module quiet zone as one path,
// in the given colour, scalable (no fixed pixel size: CSS sizes it).
func (c *Code) SVG(color string) string {
	q := 4
	w := c.Size + 2*q
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img">`, w, w)
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#fff"/><path fill="%s" d="`, w, w, color)
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; x++ {
			if c.mod[y][x] {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x+q, y+q)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}
