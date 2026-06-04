package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"
)

// FrameInfo describes one recovered frame's position in the source file.
type FrameInfo struct {
	VideoOff  int
	VideoSize int // NAL size (excluding 4-byte length prefix)
	NALType   int
	DJMDOff   int
	DJMDSize  int
}

// ── Atom builders ───────────────────────────────────────────────────────

func mp4Box(tag string, content []byte) []byte {
	b := make([]byte, 8+len(content))
	binary.BigEndian.PutUint32(b[0:4], uint32(len(b)))
	copy(b[4:8], tag)
	copy(b[8:], content)
	return b
}

func mp4FullBox(tag string, ver byte, flags uint32, content []byte) []byte {
	b := make([]byte, 12+len(content))
	binary.BigEndian.PutUint32(b[0:4], uint32(len(b)))
	copy(b[4:8], tag)
	b[8] = ver
	b[9] = byte(flags >> 16)
	b[10] = byte(flags >> 8)
	b[11] = byte(flags)
	copy(b[12:], content)
	return b
}

func put32(buf []byte, off int, v uint32) { binary.BigEndian.PutUint32(buf[off:], v) }
func put16(buf []byte, off int, v uint16) { binary.BigEndian.PutUint16(buf[off:], v) }

// ── moov sub-atoms ──────────────────────────────────────────────────────

func makeMvhd(timescale uint32, duration uint32, nextTrackID uint32) []byte {
	// v0: creation(4) modification(4) timescale(4) duration(4)
	//     rate(4) volume(2) reserved(10) matrix(36) predefined(24) nextTrackID(4)
	d := make([]byte, 96)
	put32(d, 8, timescale)    // offset 8: timescale
	put32(d, 12, duration)    // offset 12: duration
	put32(d, 16, 0x00010000)  // rate = 1.0
	put16(d, 20, 0x0100)      // volume = 1.0
	// reserved 10 bytes at 22
	// identity matrix at offset 32
	put32(d, 32, 0x00010000)  // a
	put32(d, 48, 0x00010000)  // d
	put32(d, 64, 0x40000000)  // w
	put32(d, 92, nextTrackID)
	return mp4FullBox("mvhd", 0, 0, d)
}

func makeTkhd(trackID uint32, duration uint32, width, height uint16, isVideo bool) []byte {
	// v0: creation(4) modification(4) trackID(4) reserved(4) duration(4)
	//     reserved(8) layer(2) altgroup(2) volume(2) reserved(2) matrix(36) width(4) height(4)
	d := make([]byte, 84)
	put32(d, 8, trackID)       // offset 8: track_ID
	put32(d, 16, duration)     // offset 16: duration
	// reserved(8) + layer(2) + altgroup(2) + volume(2) + reserved(2) at 20-35
	// identity matrix at offset 36
	put32(d, 36, 0x00010000)
	put32(d, 52, 0x00010000)
	put32(d, 68, 0x40000000)
	// width and height at 72, 76 as 16.16 fixed point
	if isVideo {
		put32(d, 76, uint32(width)<<16)
		put32(d, 80, uint32(height)<<16)
	}
	return mp4FullBox("tkhd", 0, 3, d)
}

func makeMdhd(timescale, duration uint32) []byte {
	// v0: creation(4) modification(4) timescale(4) duration(4) language(2) predefined(2)
	d := make([]byte, 20)
	put32(d, 8, timescale)    // offset 8: timescale
	put32(d, 12, duration)    // offset 12: duration
	put16(d, 16, 0x55C4)     // language: undetermined
	return mp4FullBox("mdhd", 0, 0, d)
}

func makeHdlr(handlerType, name string) []byte {
	nameBytes := append([]byte(name), 0)
	d := make([]byte, 20+len(nameBytes))
	copy(d[4:8], handlerType)
	copy(d[20:], nameBytes)
	return mp4FullBox("hdlr", 0, 0, d)
}

func makeVmhd() []byte {
	return mp4FullBox("vmhd", 0, 1, make([]byte, 8))
}

func makeNmhd() []byte {
	return mp4FullBox("nmhd", 0, 0, nil)
}

func makeDinf() []byte {
	url := mp4FullBox("url ", 0, 1, nil) // self-contained
	drefContent := make([]byte, 4)
	put32(drefContent, 0, 1) // entry_count
	dref := mp4FullBox("dref", 0, 0, append(drefContent, url...))
	return mp4Box("dinf", dref)
}

func makeStts(count, delta uint32) []byte {
	d := make([]byte, 12)
	put32(d, 0, 1) // entry_count
	put32(d, 4, count)
	put32(d, 8, delta)
	return mp4FullBox("stts", 0, 0, d)
}

func makeStsc() []byte {
	d := make([]byte, 16)
	put32(d, 0, 1) // entry_count
	put32(d, 4, 1) // first_chunk
	put32(d, 8, 1) // samples_per_chunk
	put32(d, 12, 1) // sample_description_index
	return mp4FullBox("stsc", 0, 0, d)
}

func makeStsz(sizes []uint32) []byte {
	d := make([]byte, 8+4*len(sizes))
	put32(d, 0, 0) // sample_size = 0 (variable)
	put32(d, 4, uint32(len(sizes)))
	for i, s := range sizes {
		put32(d, 8+i*4, s)
	}
	return mp4FullBox("stsz", 0, 0, d)
}

func makeCo64(offsets []uint64) []byte {
	d := make([]byte, 4+8*len(offsets))
	put32(d, 0, uint32(len(offsets)))
	for i, o := range offsets {
		binary.BigEndian.PutUint64(d[4+i*8:], o)
	}
	return mp4FullBox("co64", 0, 0, d)
}

func makeStss(keyframes []uint32) []byte {
	d := make([]byte, 4+4*len(keyframes))
	put32(d, 0, uint32(len(keyframes)))
	for i, k := range keyframes {
		put32(d, 4+i*4, k)
	}
	return mp4FullBox("stss", 0, 0, d)
}

func makeStbl(stsd []byte, extras ...[]byte) []byte {
	var content []byte
	content = append(content, stsd...)
	for _, e := range extras {
		content = append(content, e...)
	}
	return mp4Box("stbl", content)
}

func makeMinf(mediaHdr, dinf, stbl []byte) []byte {
	var c []byte
	c = append(c, mediaHdr...)
	c = append(c, dinf...)
	c = append(c, stbl...)
	return mp4Box("minf", c)
}

func makeMdia(mdhd, hdlr, minf []byte) []byte {
	var c []byte
	c = append(c, mdhd...)
	c = append(c, hdlr...)
	c = append(c, minf...)
	return mp4Box("mdia", c)
}

func makeTrak(tkhd, mdia []byte) []byte {
	return mp4Box("trak", append(tkhd, mdia...))
}

// ── MP4 Writer ──────────────────────────────────────────────────────────

func writeMp4(outPath string, srcData []byte, frames []FrameInfo, ref *RefInfo) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	n := len(frames)
	timescale := ref.Timescale
	sampleDelta := uint32(1001) // 60000/59.94 ≈ 1001
	duration := uint32(n) * sampleDelta

	// ── Phase 1: Write ftyp ──
	ftyp := ref.Ftyp
	if ftyp == nil {
		ftyp = defaultFtyp()
	}
	if _, err := f.Write(ftyp); err != nil {
		return err
	}

	// ── Phase 2: Compute mdat size and write mdat header ──
	var mdatContentSize int64
	for i := range frames {
		mdatContentSize += int64(4 + frames[i].VideoSize) // length-prefixed NAL
		if frames[i].DJMDSize > 0 {
			mdatContentSize += int64(frames[i].DJMDSize)
		}
	}

	mdatHeaderSize := 8
	mdatTotalSize := int64(mdatHeaderSize) + mdatContentSize
	if mdatTotalSize > 0xFFFFFFFF {
		mdatHeaderSize = 16
		mdatTotalSize = int64(mdatHeaderSize) + mdatContentSize
	}

	mdatHeader := make([]byte, mdatHeaderSize)
	if mdatHeaderSize == 16 {
		put32(mdatHeader, 0, 1) // extended size marker
		copy(mdatHeader[4:8], "mdat")
		binary.BigEndian.PutUint64(mdatHeader[8:16], uint64(mdatTotalSize))
	} else {
		put32(mdatHeader, 0, uint32(mdatTotalSize))
		copy(mdatHeader[4:8], "mdat")
	}
	if _, err := f.Write(mdatHeader); err != nil {
		return err
	}

	// ── Phase 3: Write mdat content, recording offsets ──
	baseOffset := int64(len(ftyp) + mdatHeaderSize)
	videoOffsets := make([]uint64, n)
	videoSizes := make([]uint32, n)
	djmdOffsets := make([]uint64, 0, n)
	djmdSizes := make([]uint32, 0, n)
	var keyframes []uint32
	var currentOffset int64

	start := time.Now()
	for i, fr := range frames {
		// Video sample: 4-byte length prefix + NAL data
		videoOffsets[i] = uint64(baseOffset + currentOffset)
		sampleSize := uint32(4 + fr.VideoSize)
		videoSizes[i] = sampleSize

		if _, err := f.Write(srcData[fr.VideoOff : fr.VideoOff+4+fr.VideoSize]); err != nil {
			return err
		}
		currentOffset += int64(sampleSize)

		if fr.NALType == 19 || fr.NALType == 20 {
			keyframes = append(keyframes, uint32(i+1)) // 1-based
		}

		// DJMD sample
		if fr.DJMDSize > 0 {
			djmdOffsets = append(djmdOffsets, uint64(baseOffset+currentOffset))
			djmdSizes = append(djmdSizes, uint32(fr.DJMDSize))
			if _, err := f.Write(srcData[fr.DJMDOff : fr.DJMDOff+fr.DJMDSize]); err != nil {
				return err
			}
			currentOffset += int64(fr.DJMDSize)
		}

		if i%5000 == 0 && i > 0 {
			pct := 100.0 * float64(i) / float64(n)
			fmt.Printf("\r  Writing mdat... %5.1f%%", pct)
		}
	}
	fmt.Printf("\r  Writing mdat... done (%d frames)    \n", n)

	// ── Phase 4: Build and write moov ──
	fmt.Print("  Building moov...")

	// Video track stbl
	videoStbl := makeStbl(
		ref.VideoStsd,
		makeStts(uint32(n), sampleDelta),
		makeStsc(),
		makeStsz(videoSizes),
		makeCo64(videoOffsets),
		makeStss(keyframes),
	)
	videoMinf := makeMinf(makeVmhd(), makeDinf(), videoStbl)
	videoMdia := makeMdia(
		makeMdhd(timescale, duration),
		makeHdlr("vide", "VideoHandler"),
		videoMinf,
	)
	videoTrak := makeTrak(
		makeTkhd(1, duration, ref.Width, ref.Height, true),
		videoMdia,
	)

	// DJMD track (if available)
	var djmdTrak []byte
	if len(djmdSizes) > 0 && ref.DJMDStsd != nil {
		djmdStbl := makeStbl(
			ref.DJMDStsd,
			makeStts(uint32(len(djmdSizes)), sampleDelta),
			makeStsc(),
			makeStsz(djmdSizes),
			makeCo64(djmdOffsets),
		)
		djmdMinf := makeMinf(makeNmhd(), makeDinf(), djmdStbl)
		djmdMdia := makeMdia(
			makeMdhd(timescale, duration),
			makeHdlr("meta", "DJI Meta"),
			djmdMinf,
		)
		djmdTrak = makeTrak(
			makeTkhd(2, duration, 0, 0, false),
			djmdMdia,
		)
	}

	mvhd := makeMvhd(timescale, duration, 3)
	var moovContent []byte
	moovContent = append(moovContent, mvhd...)
	moovContent = append(moovContent, videoTrak...)
	if djmdTrak != nil {
		moovContent = append(moovContent, djmdTrak...)
	}
	moov := mp4Box("moov", moovContent)

	if _, err := f.Write(moov); err != nil {
		return err
	}

	elapsed := time.Since(start)
	fmt.Printf(" done (%.0f KB, %s)\n", float64(len(moov))/1024, elapsed.Round(time.Millisecond))

	return nil
}

func defaultFtyp() []byte {
	d := make([]byte, 12)
	copy(d[0:4], "isom")
	put32(d, 4, 0x200)
	copy(d[8:12], "isom")
	return mp4Box("ftyp", d)
}

// getDJMDSize returns the expected DJMD size for frame i using reference data.
func getDJMDSize(ref *RefInfo, frameIdx int) int {
	if ref.DJMDSizes == nil {
		return 0
	}
	if frameIdx < len(ref.DJMDSizes) {
		return int(ref.DJMDSizes[frameIdx])
	}
	// Beyond reference: use median size (953 typical)
	return 953
}

// Verify io.Writer is used
var _ io.Writer = (*os.File)(nil)
