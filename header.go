package main

import (
	"bytes"
	"errors"
	"strconv"
)

type Platform int

const (
	MGI Platform = iota
	Illumina
	SurfSeq
)

var errBadHeader = errors.New("unrecognized header")

// SurfSeq header 元组：
type fields struct {
	instrument string
	run        string
	fc         string
	lane       int
	row        int
	col        int
	sub        int
	clust      int
	read       int
	filtered   byte
	control    string
	index      string
}

type Options struct {
	To Platform
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func atoi(b []byte) (int, error) {
	v := 0
	if len(b) == 0 {
		return 0, errBadHeader
	}
	for _, c := range b {
		if !isDigit(c) {
			return 0, errBadHeader
		}
		v = v*10 + int(c-'0')
	}
	return v, nil
}

// detectPlatform ：surfconv 只接受 SurfSeq
func detectPlatform(line []byte) Platform {
	if bytes.IndexByte(line, ' ') < 0 {
		return MGI
	}
	sp := bytes.IndexByte(line, ' ')
	first := line[:sp]
	n := bytes.Count(first, []byte{':'})
	if n >= 4 {
		start := 0
		for i := 0; i < 3; i++ {
			idx := bytes.IndexByte(first[start:], ':')
			if idx < 0 {
				return Illumina
			}
			start += idx + 1
		}
		end := bytes.IndexByte(first[start:], ':')
		var seg []byte
		if end < 0 {
			seg = first[start:]
		} else {
			seg = first[start : start+end]
		}
		if len(seg) > 1 && seg[0] == 'L' {
			return SurfSeq
		}
	}
	return Illumina
}

func parseSurfSeq(line []byte, f *fields) error {
	s := line
	if len(s) > 0 && s[0] == '@' {
		s = s[1:]
	}
	sp := bytes.IndexByte(s, ' ')
	if sp < 0 {
		return errBadHeader
	}
	first := s[:sp]
	second := s[sp+1:]
	parts := bytes.Split(first, []byte{':'})
	if len(parts) < 7 {
		return errBadHeader
	}
	f.instrument = string(parts[0])
	f.run = string(parts[1])
	f.fc = string(parts[2])
	laneSeg := parts[3]
	if len(laneSeg) < 2 || laneSeg[0] != 'L' {
		return errBadHeader
	}
	var err error
	if f.lane, err = atoi(laneSeg[1:]); err != nil {
		return err
	}
	rc := parts[4]
	if len(rc) < 2 || rc[0] != 'R' {
		return errBadHeader
	}
	ci := bytes.IndexByte(rc, 'C')
	if ci < 0 {
		return errBadHeader
	}
	srow, err := atoi(rc[1:ci])
	if err != nil {
		return err
	}
	scol, err := atoi(rc[ci+1:])
	if err != nil {
		return err
	}
	f.row = srow
	f.col = scol
	if f.sub, err = atoi(parts[5]); err != nil {
		return err
	}
	if f.clust, err = atoi(parts[6]); err != nil {
		return err
	}
	sp2 := bytes.Split(second, []byte{':'})
	if len(sp2) < 4 {
		return errBadHeader
	}
	if f.read, err = atoi(sp2[0]); err != nil {
		return err
	}
	if len(sp2[1]) > 0 {
		f.filtered = sp2[1][0]
	} else {
		f.filtered = 'N'
	}
	f.control = string(sp2[2])
	f.index = string(sp2[3])
	return nil
}

func appendPadded(dst []byte, v, width int) []byte {
	s := strconv.Itoa(v)
	for i := len(s); i < width; i++ {
		dst = append(dst, '0')
	}
	return append(dst, s...)
}

// formatHeader 按目标平台的位宽约定输出 header，保证同一输出文件内 header 定长
// （MGI 恒 31、Illumina 恒 69）。位宽表、FC 压缩与 MGI coord 采用 4+4 字符串
func formatHeader(dst []byte, f *fields, to Platform, fcCode []byte) []byte {
	switch to {
	case MGI:
		dst = append(dst, '@')
		dst = append(dst, fcCode...)
		dst = append(dst, 'L')
		dst = appendPadded(dst, f.lane, 1)
		dst = append(dst, 'C')
		dst = appendPadded(dst, f.col, 3)
		dst = append(dst, 'R')
		dst = appendPadded(dst, f.row, 3)
		dst = appendPadded(dst, f.sub, 4)
		dst = appendPadded(dst, f.clust, 4)
		dst = append(dst, '/')
		dst = appendPadded(dst, f.read, 1)
	case Illumina:
		dst = append(dst, '@')
		dst = append(dst, f.instrument...)
		dst = append(dst, ':')
		dst = append(dst, f.run...)
		dst = append(dst, ':')
		dst = append(dst, fcCode...)
		dst = append(dst, ':')
		dst = appendPadded(dst, f.lane, 1)
		dst = append(dst, ':')
		dst = appendPadded(dst, f.row*1000+f.col, 4)
		dst = append(dst, ':')
		dst = appendPadded(dst, f.sub, 4)
		dst = append(dst, ':')
		dst = appendPadded(dst, f.clust, 4)
		dst = append(dst, ' ')
		dst = appendPadded(dst, f.read, 1)
		dst = append(dst, ':')
		dst = append(dst, f.filtered)
		dst = append(dst, ':')
		dst = append(dst, f.control...)
		dst = append(dst, ':')
		dst = append(dst, f.index...)
	}
	return dst
}

func convertHeader(dst, line []byte, o *Options) ([]byte, error) {
	var f fields
	if err := parseSurfSeq(line, &f); err != nil {
		return nil, err
	}
	fcCode, err := encodeFC(f.fc)
	if err != nil {
		return nil, err
	}
	return formatHeader(dst, &f, o.To, []byte(fcCode)), nil
}
