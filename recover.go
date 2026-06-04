package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"
	"time"
)

// RecoverStats holds extraction statistics.
type RecoverStats struct {
	Frames      int
	Keyframes   int
	VideoBytes  int64
	LargeGaps   int
	IDRRestarts int
	GapMedian   int
	GapMin      int
	GapMax      int
}

var startCode = []byte{0x00, 0x00, 0x00, 0x01}

// ── NAL validation ──────────────────────────────────────────────────────

func checkNAL(data []byte, pos, nalMin, nalMax int) (size, nalType, tid int, ok bool) {
	if pos < 0 || pos+7 >= len(data) {
		return
	}
	ns := int(binary.BigEndian.Uint32(data[pos : pos+4]))
	if ns < nalMin || ns > nalMax || pos+4+ns > len(data) {
		return
	}
	b0, b1, b2 := data[pos+4], data[pos+5], data[pos+6]
	if b0&0x80 != 0 {
		return
	}
	nt := int((b0 >> 1) & 0x3F)
	t := int(b1 & 0x07)
	if t != 1 && t != 2 {
		return
	}
	if nt != 0 && nt != 1 && nt != 19 && nt != 20 {
		return
	}
	if b2&0x80 == 0 {
		return
	}
	return ns, nt, t, true
}

func findBestNAL(data []byte, searchStart, gapTarget, window, nalMin, nalMax int) (pos, size, nalType, tid int, ok bool) {
	bestDelta := window + 1
	lo := gapTarget - window
	if lo < 0 {
		lo = 0
	}
	for off := lo; off < gapTarget+window; off++ {
		p := searchStart + off
		ns, nt, t, valid := checkNAL(data, p, nalMin, nalMax)
		if !valid {
			continue
		}
		delta := off - gapTarget
		if delta < 0 {
			delta = -delta
		}
		if delta < bestDelta {
			bestDelta = delta
			pos, size, nalType, tid, ok = p, ns, nt, t, true
		}
	}
	return
}

func findFirstIDR(data []byte, ref *RefInfo) int {
	limit := 1 << 20
	if limit > len(data) {
		limit = len(data)
	}
	idrMin := ref.NALMax / 4
	window := ref.GapMedian / 2
	if window < 3000 {
		window = 3000
	}
	for pos := 0; pos < limit; pos++ {
		ns, nt, _, ok := checkNAL(data, pos, ref.NALMin, ref.NALMax)
		if ok && (nt == 19 || nt == 20) && ns > idrMin {
			_, _, _, _, chain := findBestNAL(data, pos+4+ns, ref.GapMedian, window, ref.NALMin, ref.NALMax)
			if chain {
				return pos
			}
		}
	}
	return -1
}

// ── Frame scanning ──────────────────────────────────────────────────────

func scanFrames(data []byte, ref *RefInfo, startOff int, verbose bool) ([]FrameInfo, RecoverStats) {
	fileSize := len(data)
	nalMin, nalMax := ref.NALMin, ref.NALMax
	gapMedian := ref.GapMedian
	pos := startOff

	var frames []FrameInfo
	var stats RecoverStats
	var gapHistory []int
	lastReport := time.Now()
	startTime := time.Now()

	for pos+7 < fileSize {
		ns, nt, _, valid := checkNAL(data, pos, nalMin, nalMax)

		if !valid {
			gt := gapMedian
			if len(gapHistory) == 0 && len(frames) == 0 {
				gt = gapMedian + 1200
			} else if len(gapHistory) >= 5 {
				gt = medianInt(gapHistory[max(0, len(gapHistory)-20):])
			}

			wideWindow := ref.GapMedian / 2
			if wideWindow < 3000 {
				wideWindow = 3000
			}

			// Tier 1: narrow window (±2000)
			p, s, t, _, found := findBestNAL(data, pos, gt, 2000, nalMin, nalMax)

			// Tier 2: wider window (±half gap) for variable metadata
			if !found {
				p, s, t, _, found = findBestNAL(data, pos, gt, wideWindow, nalMin, nalMax)
			}

			// Tier 3: wide search with backfill
			if !found {
				var farP, farS, farT int
				for probe := gt + wideWindow; probe < 2_000_000; probe++ {
					pp := pos + probe
					pns, pnt, _, pok := checkNAL(data, pp, nalMin, nalMax)
					if !pok {
						continue
					}
					_, _, _, _, chain := findBestNAL(data, pp+4+pns, gapMedian, wideWindow, nalMin, nalMax)
					if chain {
						farP, farS, farT = pp, pns, pnt
						found = true
						break
					}
				}

				if found {
					// Backfill: scan the gap for any valid frames we'd skip
					backfilled := 0
					for scan := pos + gapMedian/2; scan < farP-nalMin; scan++ {
						bns, bnt, _, bok := checkNAL(data, scan, nalMin, nalMax)
						if !bok {
							continue
						}
						_, _, _, _, bchain := findBestNAL(data, scan+4+bns, gapMedian, wideWindow, nalMin, nalMax)
						if !bchain {
							continue
						}
						fi := FrameInfo{
							VideoOff:  scan,
							VideoSize: bns,
							NALType:   bnt,
						}
						djmdStart := scan + 4 + bns
						djmdSz := getDJMDSize(ref, len(frames))
						if djmdSz > 0 && djmdStart+djmdSz <= fileSize {
							fi.DJMDOff = djmdStart
							fi.DJMDSize = djmdSz
						}
						if bnt == 19 || bnt == 20 {
							stats.Keyframes++
						}
						stats.VideoBytes += int64(bns)
						frames = append(frames, fi)
						backfilled++
						scan = scan + 4 + bns
					}

					stats.LargeGaps++
					if farT == 19 || farT == 20 {
						stats.IDRRestarts++
					}
					if verbose {
						fmt.Printf("  [gap] frame %d: +%d bytes at 0x%x (type=%d, backfilled=%d)\n",
							len(frames), farP-pos, farP, farT, backfilled)
					}
					p, s, t = farP, farS, farT
				}
			}

			if !found {
				pct := 100.0 * float64(pos) / float64(fileSize)
				fmt.Printf("\r  Chain ended at %.1f%% (0x%x)                    \n", pct, pos)
				break
			}

			gapHistory = append(gapHistory, p-pos)
			pos, ns, nt = p, s, t
		}

		// Record frame
		fi := FrameInfo{
			VideoOff:  pos,
			VideoSize: ns,
			NALType:   nt,
		}

		// DJMD: starts right after video NAL
		djmdStart := pos + 4 + ns
		djmdSz := getDJMDSize(ref, len(frames))
		if djmdSz > 0 && djmdStart+djmdSz <= fileSize {
			fi.DJMDOff = djmdStart
			fi.DJMDSize = djmdSz
		}

		if nt == 19 || nt == 20 {
			stats.Keyframes++
		}
		stats.VideoBytes += int64(ns)
		frames = append(frames, fi)

		if time.Since(lastReport) > 400*time.Millisecond {
			pct := 100.0 * float64(pos) / float64(fileSize)
			elapsed := time.Since(startTime).Seconds()
			var eta string
			if pct > 1 {
				rem := elapsed * (100 - pct) / pct
				eta = fmt.Sprintf("ETA %d:%02d", int(rem)/60, int(rem)%60)
			}
			mb := float64(stats.VideoBytes) / (1024 * 1024)
			fmt.Printf("\r  %s %5.1f%%  %d frames  %.0f MB  %s  ",
				progressBar(pct, 30), pct, len(frames), mb, eta)
			lastReport = time.Now()
		}

		pos = pos + 4 + ns
	}

	stats.Frames = len(frames)
	fmt.Printf("\r  %s 100.0%%  %d frames  %.0f MB  done          \n",
		progressBar(100, 30), stats.Frames, float64(stats.VideoBytes)/(1024*1024))

	if len(gapHistory) > 0 {
		sorted := make([]int, len(gapHistory))
		copy(sorted, gapHistory)
		sort.Ints(sorted)
		stats.GapMedian = sorted[len(sorted)/2]
		stats.GapMin = sorted[0]
		stats.GapMax = sorted[len(sorted)-1]
	}

	return frames, stats
}

// ── H265 output ─────────────────────────────────────────────────────────

func writeH265(outPath string, srcData []byte, frames []FrameInfo, ref *RefInfo) error {
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()

	for _, fr := range frames {
		if fr.NALType == 19 || fr.NALType == 20 {
			for _, p := range ref.Params {
				f.Write(startCode)
				f.Write(p.Data)
			}
		}
		f.Write(startCode)
		f.Write(srcData[fr.VideoOff+4 : fr.VideoOff+4+fr.VideoSize])
	}
	return nil
}

// ── Helpers ─────────────────────────────────────────────────────────────

func medianInt(v []int) int {
	if len(v) == 0 {
		return 0
	}
	tmp := make([]int, len(v))
	copy(tmp, v)
	sort.Ints(tmp)
	return tmp[len(tmp)/2]
}

func progressBar(pct float64, width int) string {
	filled := int(pct / 100.0 * float64(width))
	if filled > width {
		filled = width
	}
	bar := make([]byte, width)
	for i := range bar {
		if i < filled {
			bar[i] = '#'
		} else {
			bar[i] = '-'
		}
	}
	return "[" + string(bar) + "]"
}
