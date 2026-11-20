package qrcode

import (
	"fmt"
	"strings"
	"testing"
)

// decodeAndVerify performs a full ISO/IEC 18004 round-trip decode on a generated QR Matrix:
// 1. Reads & verifies both copies of 15-bit format info (checking EC level M and mask).
// 2. Reads & verifies 18-bit version info for V7..10.
// 3. Extracts data bits in reverse zigzag order, unmasks them, and packs into codewords.
// 4. De-interleaves data and EC blocks and evaluates Reed-Solomon syndromes at alpha^0..alpha^(ecLen-1).
// 5. Decodes the Byte-mode payload and returns the recovered string.
func decodeAndVerify(t *testing.T, m *Matrix) string {
	t.Helper()
	spec := levelMSpecs[m.Version-1]
	n := m.Size

	// 1. Read both format copies
	coordsTL := [15][2]int{
		{8, 0}, {8, 1}, {8, 2}, {8, 3}, {8, 4}, {8, 5}, {8, 7}, {8, 8},
		{7, 8}, {5, 8}, {4, 8}, {3, 8}, {2, 8}, {1, 8}, {0, 8},
	}
	var fmtTL, fmtSplit int
	for i, pos := range coordsTL {
		fmtTL <<= 1
		if m.Modules[pos[0]][pos[1]] {
			fmtTL |= 1
		}
		_ = i
	}
	for i := 0; i < 7; i++ {
		fmtSplit <<= 1
		if m.Modules[n-1-i][8] {
			fmtSplit |= 1
		}
	}
	for i := 0; i < 8; i++ {
		fmtSplit <<= 1
		if m.Modules[8][n-8+i] {
			fmtSplit |= 1
		}
	}
	if fmtTL != fmtSplit {
		t.Fatalf("V%d: format copies mismatch: TL=%015b Split=%015b", m.Version, fmtTL, fmtSplit)
	}
	unmaskedFmt := fmtTL ^ 0x5412
	if rem := bchRemainder(unmaskedFmt, 0x537, 10); rem != 0 {
		t.Fatalf("V%d: format BCH check failed: rem=%d", m.Version, rem)
	}
	ecLevel := (unmaskedFmt >> 13) & 0x3
	mask := (unmaskedFmt >> 10) & 0x7
	if ecLevel != 0 {
		t.Fatalf("V%d: expected EC level M (00), got %02b", m.Version, ecLevel)
	}
	if mask != m.Mask {
		t.Fatalf("V%d: decoded mask %d != Matrix.Mask %d", m.Version, mask, m.Mask)
	}

	// Dark module check
	if !m.Modules[n-8][8] {
		t.Fatalf("V%d: missing dark module at (%d, 8)", m.Version, n-8)
	}

	// 2. Check version info for V7+
	if m.Version >= 7 {
		var verBL, verTR int
		for i := 17; i >= 0; i-- {
			a := n - 11 + (i % 3)
			b := i / 3
			verBL <<= 1
			if m.Modules[a][b] {
				verBL |= 1
			}
			verTR <<= 1
			if m.Modules[b][a] {
				verTR |= 1
			}
		}
		if verBL != verTR || (verBL>>12) != m.Version || bchRemainder(verBL, 0x1F25, 12) != 0 {
			t.Fatalf("V%d: version info invalid: BL=%018b TR=%018b", m.Version, verBL, verTR)
		}
	}

	// 3. Extract unmasked bits in zigzag order
	_, isFunc := buildFunctionMatrix(&spec)
	var rawBits []bool
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
				bit := m.Modules[r][c]
				if maskBit(mask, r, c) {
					bit = !bit
				}
				rawBits = append(rawBits, bit)
			}
		}
		dirUp = !dirUp
	}

	totalBlocks := spec.g1Blocks + spec.g2Blocks
	totalCodewords := spec.dataCW + totalBlocks*spec.ecPerBlock
	if len(rawBits) != totalCodewords*8+spec.remainder {
		t.Fatalf("V%d: extracted %d bits, expected %d", m.Version, len(rawBits), totalCodewords*8+spec.remainder)
	}

	codewords := make([]byte, totalCodewords)
	for i := 0; i < totalCodewords; i++ {
		var cw byte
		for b := 0; b < 8; b++ {
			cw <<= 1
			if rawBits[i*8+b] {
				cw |= 1
			}
		}
		codewords[i] = cw
	}

	// 4. De-interleave blocks and verify Reed-Solomon syndromes == 0
	dataBlocks := make([][]byte, totalBlocks)
	ecBlocks := make([][]byte, totalBlocks)
	maxDataLen := 0
	for b := 0; b < totalBlocks; b++ {
		l := spec.g1DataCW
		if b >= spec.g1Blocks {
			l = spec.g2DataCW
		}
		if l > maxDataLen {
			maxDataLen = l
		}
		dataBlocks[b] = make([]byte, 0, l)
		ecBlocks[b] = make([]byte, 0, spec.ecPerBlock)
	}

	idx := 0
	for col := 0; col < maxDataLen; col++ {
		for b := 0; b < totalBlocks; b++ {
			targetLen := spec.g1DataCW
			if b >= spec.g1Blocks {
				targetLen = spec.g2DataCW
			}
			if col < targetLen {
				dataBlocks[b] = append(dataBlocks[b], codewords[idx])
				idx++
			}
		}
	}
	for col := 0; col < spec.ecPerBlock; col++ {
		for b := 0; b < totalBlocks; b++ {
			ecBlocks[b] = append(ecBlocks[b], codewords[idx])
			idx++
		}
	}

	var stream []byte
	for b := 0; b < totalBlocks; b++ {
		fullBlock := append(append([]byte{}, dataBlocks[b]...), ecBlocks[b]...)
		for root := 0; root < spec.ecPerBlock; root++ {
			alpha := gfExp[root]
			var eval byte
			for _, coef := range fullBlock {
				eval = gfMul(eval, alpha) ^ coef
			}
			if eval != 0 {
				t.Fatalf("V%d block %d: RS syndrome at root alpha^%d = %d != 0", m.Version, b, root, eval)
			}
		}
		stream = append(stream, dataBlocks[b]...)
	}

	// 5. Decode Byte-mode payload from stream
	bitPos := 0
	readBits := func(cnt int) int {
		v := 0
		for i := 0; i < cnt; i++ {
			byteIdx := bitPos / 8
			bitOff := 7 - (bitPos % 8)
			v = (v << 1) | int((stream[byteIdx]>>bitOff)&1)
			bitPos++
		}
		return v
	}

	mode := readBits(4)
	if mode != 0b0100 {
		t.Fatalf("V%d: expected Byte mode 0100, got %04b", m.Version, mode)
	}
	ccBits := 8
	if m.Version >= 10 {
		ccBits = 16
	}
	length := readBits(ccBits)
	out := make([]byte, length)
	for i := 0; i < length; i++ {
		out[i] = byte(readBits(8))
	}
	return string(out)
}

func TestEncodeAndRoundTripVersions1Through10(t *testing.T) {
	// Lengths chosen to exercise every version from V1 through V10 (including 2-group blocks V8-V10 and V7+ version info).
	testLengths := []int{10, 20, 35, 55, 75, 98, 118, 140, 170, 205}
	for idx, l := range testLengths {
		expectedVersion := idx + 1
		base := "http://192.168.1.105:52341/download/abc123def456/WeChat_8.0.50.ipa?"
		payload := base
		for len(payload) < l {
			payload += "x"
		}
		payload = payload[:l]

		m, err := Encode(payload)
		if err != nil {
			t.Fatalf("Encode len=%d failed: %v", l, err)
		}
		if m.Version != expectedVersion {
			t.Fatalf("len=%d got version %d, want %d", l, m.Version, expectedVersion)
		}
		decoded := decodeAndVerify(t, m)
		if decoded != payload {
			t.Fatalf("V%d round-trip mismatch: got %q, want %q", m.Version, decoded, payload)
		}
	}
}

func TestToSVG(t *testing.T) {
	u := "http://192.168.31.88:61234/download/9f8e7d6c5b4a/TestApp.ipa"
	svg, err := ToSVG(u, 6)
	if err != nil {
		t.Fatalf("ToSVG error: %v", err)
	}
	if !strings.HasPrefix(svg, "<svg ") || !strings.HasSuffix(svg, "</svg>") {
		t.Fatalf("unexpected SVG wrapper: %s", svg[:min(80, len(svg))])
	}
	if !strings.Contains(svg, `<path fill="#111827"`) {
		t.Fatalf("SVG missing dark module path")
	}

	// Capacity error check
	_, err = Encode(strings.Repeat("a", 250))
	if err == nil {
		t.Fatalf("expected error for >213 byte input, got nil")
	}
	_ = fmt.Sprintf("")
}
