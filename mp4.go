package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
)

// NALParam holds a codec parameter set (VPS/SPS/PPS) extracted from hvcC.
type NALParam struct {
	Type byte
	Data []byte
}

// ── Top-level atom search ───────────────────────────────────────────────

func findTopLevelAtom(data []byte, name string) ([]byte, error) {
	tag := []byte(name)
	pos := 0
	for pos+8 <= len(data) {
		sz := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		if sz == 1 && pos+16 <= len(data) {
			sz = int(binary.BigEndian.Uint64(data[pos+8 : pos+16]))
		}
		if sz < 8 || pos+sz > len(data) {
			break
		}
		if bytes.Equal(data[pos+4:pos+8], tag) {
			return data[pos : pos+sz], nil
		}
		pos += sz
	}
	return nil, fmt.Errorf("atom '%s' not found", name)
}

func findChildAtoms(container []byte, name string) [][]byte {
	tag := []byte(name)
	var out [][]byte
	pos := 8
	for pos+8 <= len(container) {
		sz := int(binary.BigEndian.Uint32(container[pos : pos+4]))
		if sz < 8 || pos+sz > len(container) {
			break
		}
		if bytes.Equal(container[pos+4:pos+8], tag) {
			out = append(out, container[pos:pos+sz])
		}
		pos += sz
	}
	return out
}

// ── hvcC parsing ────────────────────────────────────────────────────────

func parseHvcC(trakData []byte) ([]NALParam, error) {
	idx := bytes.Index(trakData, []byte("hvcC"))
	if idx < 4 {
		return nil, fmt.Errorf("hvcC not found")
	}
	atomSz := int(binary.BigEndian.Uint32(trakData[idx-4 : idx]))
	if atomSz < 30 || idx-4+atomSz > len(trakData) {
		return nil, fmt.Errorf("invalid hvcC size")
	}
	hvcc := trakData[idx+4 : idx-4+atomSz]
	if len(hvcc) < 23 {
		return nil, fmt.Errorf("hvcC too short")
	}

	var params []NALParam
	numArrays := int(hvcc[22])
	p := 23
	for i := 0; i < numArrays && p+3 <= len(hvcc); i++ {
		arrType := hvcc[p] & 0x3F
		numNALUs := int(binary.BigEndian.Uint16(hvcc[p+1 : p+3]))
		p += 3
		for j := 0; j < numNALUs && p+2 <= len(hvcc); j++ {
			nl := int(binary.BigEndian.Uint16(hvcc[p : p+2]))
			p += 2
			if p+nl > len(hvcc) {
				break
			}
			params = append(params, NALParam{Type: arrType, Data: append([]byte{}, hvcc[p:p+nl]...)})
			p += nl
		}
	}
	if len(params) == 0 {
		return nil, fmt.Errorf("no parameter sets in hvcC")
	}
	return params, nil
}

// ── stsz parsing ────────────────────────────────────────────────────────

func parseStsz(trakData []byte) ([]uint32, error) {
	idx := bytes.Index(trakData, []byte("stsz"))
	if idx < 0 {
		return nil, fmt.Errorf("stsz not found")
	}
	base := idx + 4
	if base+12 > len(trakData) {
		return nil, fmt.Errorf("stsz too short")
	}
	fixedSize := binary.BigEndian.Uint32(trakData[base+4 : base+8])
	count := int(binary.BigEndian.Uint32(trakData[base+8 : base+12]))

	sizes := make([]uint32, count)
	if fixedSize > 0 {
		for i := range sizes {
			sizes[i] = fixedSize
		}
	} else {
		tbl := base + 12
		if tbl+count*4 > len(trakData) {
			return nil, fmt.Errorf("stsz table truncated")
		}
		for i := 0; i < count; i++ {
			sizes[i] = binary.BigEndian.Uint32(trakData[tbl+i*4 : tbl+i*4+4])
		}
	}
	return sizes, nil
}

// ── Reference file parsing ──────────────────────────────────────────────

// RefInfo holds all parameters auto-detected from the reference file.
type RefInfo struct {
	Params    []NALParam
	GapMedian int
	NALMin    int
	NALMax    int
	NumFrames int
	// MP4 muxing data (extracted from reference)
	Ftyp      []byte     // raw ftyp atom
	VideoStsd []byte     // raw stsd atom for video track
	DJMDStsd  []byte     // raw stsd atom for DJMD track
	DJMDSizes []uint32   // per-frame DJMD sizes from stsz
	Width     uint16     // video width
	Height    uint16     // video height
	Timescale uint32     // media timescale
}

func extractStsd(trak []byte) []byte {
	idx := bytes.Index(trak, []byte("stsd"))
	if idx < 4 {
		return nil
	}
	sz := int(binary.BigEndian.Uint32(trak[idx-4 : idx]))
	if sz < 16 || idx-4+sz > len(trak) {
		return nil
	}
	return append([]byte{}, trak[idx-4:idx-4+sz]...)
}

func parseReference(data []byte) (*RefInfo, error) {
	moov, err := findTopLevelAtom(data, "moov")
	if err != nil {
		return nil, err
	}

	traks := findChildAtoms(moov, "trak")
	if len(traks) == 0 {
		return nil, fmt.Errorf("no trak atoms")
	}

	var params []NALParam
	var videoTrackIdx int
	for i, trak := range traks {
		if bytes.Contains(trak, []byte("hvcC")) {
			params, err = parseHvcC(trak)
			if err != nil {
				return nil, err
			}
			videoTrackIdx = i
			break
		}
	}
	if params == nil {
		return nil, fmt.Errorf("no HEVC video track found")
	}

	var allSizes [][]uint32
	for _, trak := range traks {
		if sizes, err := parseStsz(trak); err == nil {
			allSizes = append(allSizes, sizes)
		}
	}

	info := &RefInfo{
		Params:    params,
		GapMedian: 12107,
		NALMin:    5000,
		NALMax:    800_000,
		Timescale: 60000,
		Width:     1920,
		Height:    1080,
	}

	// Extract ftyp
	if ftyp, err := findTopLevelAtom(data, "ftyp"); err == nil {
		info.Ftyp = append([]byte{}, ftyp...)
	}

	// Extract stsd from video track
	info.VideoStsd = extractStsd(traks[videoTrackIdx])

	// Find DJMD track and extract stsd + sizes
	for i, trak := range traks {
		if i == videoTrackIdx {
			continue
		}
		if bytes.Contains(trak, []byte("djmd")) {
			info.DJMDStsd = extractStsd(trak)
			if sizes, err := parseStsz(trak); err == nil {
				info.DJMDSizes = sizes
			}
			break
		}
	}

	// Extract video dimensions from hvc1 sample entry
	if info.VideoStsd != nil {
		hvc1Idx := bytes.Index(info.VideoStsd, []byte("hvc1"))
		if hvc1Idx >= 0 && hvc1Idx+40 < len(info.VideoStsd) {
			info.Width = binary.BigEndian.Uint16(info.VideoStsd[hvc1Idx+28 : hvc1Idx+30])
			info.Height = binary.BigEndian.Uint16(info.VideoStsd[hvc1Idx+30 : hvc1Idx+32])
		}
	}

	// Compute metadata gap
	if len(allSizes) >= 2 {
		minLen := len(allSizes[1])
		for i := 2; i < len(allSizes); i++ {
			if len(allSizes[i]) < minLen {
				minLen = len(allSizes[i])
			}
		}
		gaps := make([]int, minLen)
		for j := 0; j < minLen; j++ {
			for i := 1; i < len(allSizes); i++ {
				gaps[j] += int(allSizes[i][j])
			}
		}
		sort.Ints(gaps)
		info.GapMedian = gaps[len(gaps)/2]
	}

	// NAL size range from video stsz
	if videoTrackIdx < len(allSizes) {
		vSizes := allSizes[videoTrackIdx]
		info.NumFrames = len(vSizes)
		if len(vSizes) > 0 {
			sorted := make([]int, len(vSizes))
			for i, s := range vSizes {
				sorted[i] = int(s)
			}
			sort.Ints(sorted)
			p1 := sorted[len(sorted)/100]
			p99 := sorted[99*len(sorted)/100]
			info.NALMin = p1 / 2
			info.NALMax = p99 * 2
			if info.NALMin < 1000 {
				info.NALMin = 1000
			}
		}
	}

	return info, nil
}

