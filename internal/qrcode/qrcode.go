package qrcode

import (
	"errors"
	"fmt"
	"strings"
)

// Error Correction Level M parameters for Versions 1..10 (ISO/IEC 18004).
type versionSpec struct {
	version      int
	size         int
	dataCW       int
	ecPerBlock   int
	g1Blocks     int
	g1DataCW     int
	g2Blocks     int
	g2DataCW     int
	alignCenters []int
	remainder    int
}

var levelMSpecs = []versionSpec{
	{version: 1, size: 21, dataCW: 16, ecPerBlock: 10, g1Blocks: 1, g1DataCW: 16, alignCenters: nil, remainder: 0},
	{version: 2, size: 25, dataCW: 28, ecPerBlock: 16, g1Blocks: 1, g1DataCW: 28, alignCenters: []int{6, 18}, remainder: 7},
	{version: 3, size: 29, dataCW: 44, ecPerBlock: 26, g1Blocks: 1, g1DataCW: 44, alignCenters: []int{6, 22}, remainder: 7},
	{version: 4, size: 33, dataCW: 64, ecPerBlock: 18, g1Blocks: 2, g1DataCW: 32, alignCenters: []int{6, 26}, remainder: 7},
	{version: 5, size: 37, dataCW: 86, ecPerBlock: 24, g1Blocks: 2, g1DataCW: 43, alignCenters: []int{6, 30}, remainder: 7},
	{version: 6, size: 41, dataCW: 108, ecPerBlock: 16, g1Blocks: 4, g1DataCW: 27, alignCenters: []int{6, 34}, remainder: 7},
	{version: 7, size: 45, dataCW: 124, ecPerBlock: 18, g1Blocks: 4, g1DataCW: 31, alignCenters: []int{6, 22, 38}, remainder: 0},
	{version: 8, size: 49, dataCW: 154, ecPerBlock: 22, g1Blocks: 2, g1DataCW: 38, g2Blocks: 2, g2DataCW: 39, alignCenters: []int{6, 24, 42}, remainder: 0},
	{version: 9, size: 53, dataCW: 182, ecPerBlock: 22, g1Blocks: 3, g1DataCW: 36, g2Blocks: 2, g2DataCW: 37, alignCenters: []int{6, 26, 46}, remainder: 0},
	{version: 10, size: 57, dataCW: 216, ecPerBlock: 26, g1Blocks: 4, g1DataCW: 43, g2Blocks: 1, g2DataCW: 44, alignCenters: []int{6, 28, 50}, remainder: 0},
}

// GF(256) lookup tables using primitive polynomial 0x11d.
var gfExp [512]byte
var gfLog [256]byte

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		gfExp[i] = byte(x)
		gfLog[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11d
		}
	}
	for i := 255; i < 512; i++ {
		gfExp[i] = gfExp[i-255]
	}
}

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

func rsGeneratorPoly(degree int) []byte {
	poly := []byte{1}
	for i := 0; i < degree; i++ {
		next := make([]byte, len(poly)+1)
		factor := gfExp[i]
		for j, coef := range poly {
			next[j] ^= coef
			next[j+1] ^= gfMul(coef, factor)
		}
		poly = next
	}
	return poly
}

func rsRemainder(data []byte, ecLen int) []byte {
	gen := rsGeneratorPoly(ecLen)
	rem := make([]byte, len(data)+ecLen)
	copy(rem, data)
	for i := 0; i < len(data); i++ {
		coef := rem[i]
		if coef == 0 {
			continue
		}
		for j := 0; j < len(gen); j++ {
			rem[i+j] ^= gfMul(gen[j], coef)
		}
	}
	return rem[len(data):]
}

// Matrix represents an encoded QR code module grid (true = dark module).
type Matrix struct {
	Version int
	Size    int
	Mask    int
	Modules [][]bool
}

// Encode encodes text in Byte mode at Error Correction Level M (Versions 1..10).
func Encode(text string) (*Matrix, error) {
	payload := []byte(text)
	var spec *versionSpec
	for i := range levelMSpecs {
		s := &levelMSpecs[i]
		ccBits := 8
		if s.version >= 10 {
			ccBits = 16
		}
		neededBits := 4 + ccBits + len(payload)*8
		if neededBits <= s.dataCW*8 {
			spec = s
			break
		}
	}
	if spec == nil {
		return nil, fmt.Errorf("input length %d bytes exceeds QR Version 10-M capacity (213 bytes)", len(payload))
	}

	codewords := encodeDataCodewords(payload, spec)
	finalBits := interleaveAndAddEC(codewords, spec)

	bestScore := int(^uint(0) >> 1)
	var bestGrid [][]bool
	bestMask := 0

	for mask := 0; mask < 8; mask++ {
		grid, isFunc := buildFunctionMatrix(spec)
		placeDataBits(grid, isFunc, finalBits, mask)
		writeFormatInfo(grid, mask)
		if spec.version >= 7 {
			writeVersionInfo(grid, spec.version)
		}
		score := penaltyScore(grid)
		if score < bestScore {
			bestScore = score
			bestGrid = grid
			bestMask = mask
		}
	}

	return &Matrix{
		Version: spec.version,
		Size:    spec.size,
		Mask:    bestMask,
		Modules: bestGrid,
	}, nil
}

// ToSVG renders text as a crisp self-contained SVG QR code with a 4-module quiet zone.
func ToSVG(text string, modulePx int) (string, error) {
	if modulePx <= 0 {
		modulePx = 6
	}
	m, err := Encode(text)
	if err != nil {
		return "", err
	}
	const quiet = 4
	total := (m.Size + quiet*2) * modulePx

	var b strings.Builder
	b.Grow(4096)
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" shape-rendering="crispEdges">`,
		total, total, total, total)
	b.WriteString(`<rect width="100%" height="100%" fill="#ffffff"/>`)
	b.WriteString(`<path fill="#111827" d="`)
	for r := 0; r < m.Size; r++ {
		for c := 0; c < m.Size; c++ {
			if m.Modules[r][c] {
				x := (c + quiet) * modulePx
				y := (r + quiet) * modulePx
				fmt.Fprintf(&b, "M%d,%dh%dv%dh-%dz", x, y, modulePx, modulePx, modulePx)
			}
		}
	}
	b.WriteString(`"/>`)
	b.WriteString(`</svg>`)
	return b.String(), nil
}

type bitWriter struct {
	bits []bool
}

func (bw *bitWriter) write(val int, count int) {
	for i := count - 1; i >= 0; i-- {
		bw.bits = append(bw.bits, ((val>>i)&1) == 1)
	}
}

func encodeDataCodewords(payload []byte, spec *versionSpec) []byte {
	var bw bitWriter
	bw.write(0b0100, 4) // Byte mode indicator
	ccBits := 8
	if spec.version >= 10 {
		ccBits = 16
	}
	bw.write(len(payload), ccBits)
	for _, ch := range payload {
		bw.write(int(ch), 8)
	}

	capacityBits := spec.dataCW * 8
	term := capacityBits - len(bw.bits)
	if term > 4 {
		term = 4
	}
	for i := 0; i < term; i++ {
		bw.bits = append(bw.bits, false)
	}
	for len(bw.bits)%8 != 0 {
		bw.bits = append(bw.bits, false)
	}

	out := make([]byte, 0, spec.dataCW)
	for i := 0; i < len(bw.bits); i += 8 {
		var v byte
		for j := 0; j < 8; j++ {
			v <<= 1
			if bw.bits[i+j] {
				v |= 1
			}
		}
		out = append(out, v)
	}

	padBytes := []byte{0xEC, 0x11}
	for idx := 0; len(out) < spec.dataCW; idx++ {
		out = append(out, padBytes[idx%2])
	}
	return out
}

func interleaveAndAddEC(dataCW []byte, spec *versionSpec) []bool {
	totalBlocks := spec.g1Blocks + spec.g2Blocks
	dataBlocks := make([][]byte, totalBlocks)
	ecBlocks := make([][]byte, totalBlocks)

	offset := 0
	maxDataLen := 0
	for i := 0; i < totalBlocks; i++ {
		blockLen := spec.g1DataCW
		if i >= spec.g1Blocks {
			blockLen = spec.g2DataCW
		}
		if blockLen > maxDataLen {
			maxDataLen = blockLen
		}
		blk := make([]byte, blockLen)
		copy(blk, dataCW[offset:offset+blockLen])
		offset += blockLen
		dataBlocks[i] = blk
		ecBlocks[i] = rsRemainder(blk, spec.ecPerBlock)
	}

	interleaved := make([]byte, 0, spec.dataCW+totalBlocks*spec.ecPerBlock)
	for col := 0; col < maxDataLen; col++ {
		for b := 0; b < totalBlocks; b++ {
			if col < len(dataBlocks[b]) {
				interleaved = append(interleaved, dataBlocks[b][col])
			}
		}
	}
	for col := 0; col < spec.ecPerBlock; col++ {
		for b := 0; b < totalBlocks; b++ {
			interleaved = append(interleaved, ecBlocks[b][col])
		}
	}

	bits := make([]bool, 0, len(interleaved)*8+spec.remainder)
	for _, cw := range interleaved {
		for i := 7; i >= 0; i-- {
			bits = append(bits, ((cw>>i)&1) == 1)
		}
	}
	for i := 0; i < spec.remainder; i++ {
		bits = append(bits, false)
	}
	return bits
}

func buildFunctionMatrix(spec *versionSpec) ([][]bool, [][]bool) {
	n := spec.size
	grid := make([][]bool, n)
	isFunc := make([][]bool, n)
	for i := 0; i < n; i++ {
		grid[i] = make([]bool, n)
		isFunc[i] = make([]bool, n)
	}

	setFunc := func(r, c int, dark bool) {
		if r >= 0 && r < n && c >= 0 && c < n {
			grid[r][c] = dark
			isFunc[r][c] = true
		}
	}

	// Finder patterns + 1-module white separators
	placeFinder := func(top, left int) {
		for dr := -1; dr <= 7; dr++ {
			for dc := -1; dc <= 7; dc++ {
				r, c := top+dr, left+dc
				if r < 0 || r >= n || c < 0 || c >= n {
					continue
				}
				dark := false
				if dr >= 0 && dr <= 6 && dc >= 0 && dc <= 6 {
					if dr == 0 || dr == 6 || dc == 0 || dc == 6 || (dr >= 2 && dr <= 4 && dc >= 2 && dc <= 4) {
						dark = true
					}
				}
				setFunc(r, c, dark)
			}
		}
	}
	placeFinder(0, 0)
	placeFinder(0, n-7)
	placeFinder(n-7, 0)

	// Timing patterns
	for i := 8; i < n-8; i++ {
		dark := i%2 == 0
		setFunc(6, i, dark)
		setFunc(i, 6, dark)
	}

	// Alignment patterns
	for _, cr := range spec.alignCenters {
		for _, cc := range spec.alignCenters {
			if (cr <= 8 && cc <= 8) || (cr <= 8 && cc >= n-9) || (cr >= n-9 && cc <= 8) {
				continue
			}
			for dr := -2; dr <= 2; dr++ {
				for dc := -2; dc <= 2; dc++ {
					dark := dr == -2 || dr == 2 || dc == -2 || dc == 2 || (dr == 0 && dc == 0)
					setFunc(cr+dr, cc+dc, dark)
				}
			}
		}
	}

	// Reserve format info zones + fixed dark module
	for i := 0; i <= 8; i++ {
		if i != 6 {
			isFunc[8][i] = true
			isFunc[i][8] = true
		}
	}
	for i := 0; i < 8; i++ {
		isFunc[8][n-1-i] = true
		if i < 7 {
			isFunc[n-1-i][8] = true
		}
	}
	setFunc(n-8, 8, true)

	// Reserve version info zones for V7+
	if spec.version >= 7 {
		for i := 0; i < 6; i++ {
			for j := 0; j < 3; j++ {
				isFunc[n-11+j][i] = true
				isFunc[i][n-11+j] = true
			}
		}
	}

	return grid, isFunc
}

func maskBit(mask, r, c int) bool {
	switch mask {
	case 0:
		return (r+c)%2 == 0
	case 1:
		return r%2 == 0
	case 2:
		return c%3 == 0
	case 3:
		return (r+c)%3 == 0
	case 4:
		return (r/2+c/3)%2 == 0
	case 5:
		return (r*c)%2+(r*c)%3 == 0
	case 6:
		return ((r*c)%2+(r*c)%3)%2 == 0
	case 7:
		return ((r+c)%2+(r*c)%3)%2 == 0
	default:
		return false
	}
}

func placeDataBits(grid [][]bool, isFunc [][]bool, bits []bool, mask int) {
	n := len(grid)
	bitIdx := 0
	dirUp := true
	for col := n - 1; col >= 1; col -= 2 {
		if col == 6 {
			col--
		}
		for step := 0; step < n; step++ {
			r := step
			if dirUp {
				r = n - 1 - step
			}
			for dc := 0; dc < 2; dc++ {
				c := col - dc
				if isFunc[r][c] {
					continue
				}
				var bit bool
				if bitIdx < len(bits) {
					bit = bits[bitIdx]
					bitIdx++
				}
				if maskBit(mask, r, c) {
					bit = !bit
				}
				grid[r][c] = bit
			}
		}
		dirUp = !dirUp
	}
}

func bchRemainder(val, poly, polyDegree int) int {
	rem := val
	for i := 31; i >= polyDegree; i-- {
		if (rem & (1 << i)) != 0 {
			rem ^= poly << (i - polyDegree)
		}
	}
	return rem
}

func formatWord(mask int) int {
	// EC Level M indicator = 00
	data := mask & 0x7
	rem := bchRemainder(data<<10, 0x537, 10)
	return ((data << 10) | rem) ^ 0x5412
}

func writeFormatInfo(grid [][]bool, mask int) {
	n := len(grid)
	word := formatWord(mask)
	bit := func(idx int) bool { return ((word >> idx) & 1) == 1 }

	// Top-left copy (MSB = bit 14 at (8,0) .. LSB = bit 0 at (0,8))
	coordsTL := [15][2]int{
		{8, 0}, {8, 1}, {8, 2}, {8, 3}, {8, 4}, {8, 5}, {8, 7}, {8, 8},
		{7, 8}, {5, 8}, {4, 8}, {3, 8}, {2, 8}, {1, 8}, {0, 8},
	}
	for i, pos := range coordsTL {
		grid[pos[0]][pos[1]] = bit(14 - i)
	}

	// Bottom-left (bits 14..8) and top-right (bits 7..0)
	for i := 0; i < 7; i++ {
		grid[n-1-i][8] = bit(14 - i)
	}
	for i := 0; i < 8; i++ {
		grid[8][n-8+i] = bit(7 - i)
	}
}

func writeVersionInfo(grid [][]bool, version int) {
	n := len(grid)
	rem := bchRemainder(version<<12, 0x1F25, 12)
	word := (version << 12) | rem
	for i := 0; i < 18; i++ {
		dark := ((word >> i) & 1) == 1
		a := n - 11 + (i % 3)
		b := i / 3
		grid[a][b] = dark
		grid[b][a] = dark
	}
}

func penaltyScore(grid [][]bool) int {
	n := len(grid)
	score := 0

	// N1: 5+ consecutive same-color modules in row or col
	for r := 0; r < n; r++ {
		runLen := 1
		for c := 1; c < n; c++ {
			if grid[r][c] == grid[r][c-1] {
				runLen++
			} else {
				if runLen >= 5 {
					score += 3 + (runLen - 5)
				}
				runLen = 1
			}
		}
		if runLen >= 5 {
			score += 3 + (runLen - 5)
		}
	}
	for c := 0; c < n; c++ {
		runLen := 1
		for r := 1; r < n; r++ {
			if grid[r][c] == grid[r-1][c] {
				runLen++
			} else {
				if runLen >= 5 {
					score += 3 + (runLen - 5)
				}
				runLen = 1
			}
		}
		if runLen >= 5 {
			score += 3 + (runLen - 5)
		}
	}

	// N2: 2x2 blocks of same color
	for r := 0; r < n-1; r++ {
		for c := 0; c < n-1; c++ {
			v := grid[r][c]
			if grid[r][c+1] == v && grid[r+1][c] == v && grid[r+1][c+1] == v {
				score += 3
			}
		}
	}

	// N3: 1:1:3:1:1 finder-like patterns with 4 light modules before or after
	matchPattern := func(seq []bool) bool {
		if len(seq) != 11 {
			return false
		}
		patA := [11]bool{true, false, true, true, true, false, true, false, false, false, false}
		patB := [11]bool{false, false, false, false, true, false, true, true, true, false, true}
		matchA, matchB := true, true
		for i := 0; i < 11; i++ {
			if seq[i] != patA[i] {
				matchA = false
			}
			if seq[i] != patB[i] {
				matchB = false
			}
		}
		return matchA || matchB
	}

	rowBuf := make([]bool, 11)
	colBuf := make([]bool, 11)
	for i := 0; i < n; i++ {
		for j := 0; j <= n-11; j++ {
			for k := 0; k < 11; k++ {
				rowBuf[k] = grid[i][j+k]
				colBuf[k] = grid[j+k][i]
			}
			if matchPattern(rowBuf) {
				score += 40
			}
			if matchPattern(colBuf) {
				score += 40
			}
		}
	}

	// N4: dark module ratio deviation from 50%
	darkCount := 0
	for r := 0; r < n; r++ {
		for c := 0; c < n; c++ {
			if grid[r][c] {
				darkCount++
			}
		}
	}
	total := n * n
	percent := (darkCount * 100) / total
	prev5 := (percent / 5) * 5
	next5 := prev5 + 5
	d1 := prev5 - 50
	if d1 < 0 {
		d1 = -d1
	}
	d2 := next5 - 50
	if d2 < 0 {
		d2 = -d2
	}
	if d2 < d1 {
		d1 = d2
	}
	score += (d1 / 5) * 10
	return score
}

var ErrUnsupportedCapacity = errors.New("input exceeds maximum QR capacity")
