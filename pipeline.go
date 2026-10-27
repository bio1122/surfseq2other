package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/klauspost/pgzip"
)

type multiCloser struct {
	io.Reader
	closers []io.Closer
}

func (m *multiCloser) Close() error {
	var err error
	for _, c := range m.closers {
		if e := c.Close(); e != nil {
			err = e
		}
	}
	return err
}

func openInput(path string) (io.ReadCloser, error) {
	if path == "/dev/stdin" || path == "-" {
		return openCompressed(os.Stdin)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	rc, err := openCompressed(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	if mc, ok := rc.(*multiCloser); ok {
		mc.closers = append(mc.closers, f)
		return mc, nil
	}
	return rc, nil
}

func openCompressed(f *os.File) (io.ReadCloser, error) {
	br := bufio.NewReaderSize(f, 1<<20)
	magic, err := br.Peek(2)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := pgzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		return &multiCloser{Reader: gz, closers: []io.Closer{gz}}, nil
	}
	return &multiCloser{Reader: br}, nil
}

func openOutput(path string, zl int) (io.WriteCloser, error) {
	if path == "/dev/stdout" || path == "-" {
		return wrapCompressed(os.Stdout, path, zl)
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	wc, err := wrapCompressed(f, path, zl)
	if err != nil {
		f.Close()
		return nil, err
	}
	if mc, ok := wc.(*writeMultiCloser); ok {
		mc.closers = append(mc.closers, f)
		return mc, nil
	}
	return wc, nil
}

type writeMultiCloser struct {
	io.Writer
	closers []io.Closer
}

func (m *writeMultiCloser) Close() error {
	var err error
	for _, c := range m.closers {
		if e := c.Close(); e != nil {
			err = e
		}
	}
	return err
}

func wrapCompressed(f *os.File, path string, zl int) (io.WriteCloser, error) {
	n := len(path)
	if n > 3 && path[n-3:] == ".gz" {
		gz, err := pgzip.NewWriterLevel(f, zl)
		if err != nil {
			return nil, err
		}
		gz.SetConcurrency(1<<20, 4)
		return &writeMultiCloser{Writer: gz, closers: []io.Closer{gz}}, nil
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	return &writeMultiCloser{Writer: bw, closers: []io.Closer{closerFunc(bw.Flush)}}, nil
}

type closerFunc func() error

func (c closerFunc) Close() error { return c() }

type chunk struct {
	seq  int
	data []byte
}

type Pipeline struct {
	Opt     *Options
	Workers int
	ChunkN  int
	// FirstFC 由 reader 在 Convert 期间记录输入首个 header 的原始芯片号，
	// main 转换完成后写 .chipmap.tsv
	FirstFC string
}

func (p *Pipeline) Convert(inPath, outPath string, zl int) error {
	in, err := openInput(inPath)
	if err != nil {
		return fmt.Errorf("open input %s: %w", inPath, err)
	}
	defer in.Close()
	out, err := openOutput(outPath, zl)
	if err != nil {
		return fmt.Errorf("open output %s: %w", outPath, err)
	}

	rawCh := make(chan chunk, p.Workers*2)
	outCh := make(chan chunk, p.Workers*2)
	errCh := make(chan error, p.Workers+2)

	var rerr error
	go func() {
		br := bufio.NewReaderSize(in, 4<<20)
		seq := 0
		buf := make([]byte, 0, 8<<20)
		recLines := 0
		first := true
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				if first {
					first = false
					if ferr := p.validateFirst(line); ferr != nil {
						rerr = ferr
						close(rawCh)
						return
					}
				}
				buf = append(buf, line...)
				recLines++
				if recLines == p.ChunkN*4 {
					rawCh <- chunk{seq: seq, data: buf}
					seq++
					buf = make([]byte, 0, 8<<20)
					recLines = 0
				}
			}
			if err != nil {
				if err != io.EOF {
					rerr = err
				}
				break
			}
		}
		if first {
			rerr = fmt.Errorf("no records: input empty")
		}
		if len(buf) > 0 {
			rawCh <- chunk{seq: seq, data: buf}
		}
		close(rawCh)
	}()

	var wgw sync.WaitGroup
	for i := 0; i < p.Workers; i++ {
		wgw.Add(1)
		go func() {
			defer wgw.Done()
			var f fields
			enc := &fcEncoder{}
			for ck := range rawCh {
				out, err := p.processChunk(ck.data, &f, enc)
				if err != nil {
					errCh <- fmt.Errorf("chunk %d: %w", ck.seq, err)
					// 排空 rawCh
					for range rawCh {
					}
					return
				}
				outCh <- chunk{seq: ck.seq, data: out}
			}
		}()
	}
	go func() {
		wgw.Wait()
		close(outCh)
	}()

	var werr error
	wdone := make(chan struct{})
	go func() {
		defer close(wdone)
		next := 0
		pending := make(map[int][]byte)
		for ck := range outCh {
			if werr != nil {
				// 写出已失败
				continue
			}
			pending[ck.seq] = ck.data
			for {
				data, ok := pending[next]
				if !ok {
					break
				}
				if _, err := out.Write(data); err != nil {
					werr = err
					break
				}
				delete(pending, next)
				next++
			}
		}
		if werr == nil {
			for seq, data := range pending {
				if seq != next {
					werr = fmt.Errorf("missing chunk %d", next)
					break
				}
				if _, err := out.Write(data); err != nil {
					werr = err
					break
				}
				next++
			}
		}
		if err := out.Close(); err != nil && werr == nil {
			werr = err
		}
	}()

	wgw.Wait()
	<-wdone
	select {
	case err := <-errCh:
		return err
	default:
	}
	if rerr != nil {
		return rerr
	}
	if werr != nil {
		return werr
	}
	return nil
}

func (p *Pipeline) processChunk(data []byte, f *fields, enc *fcEncoder) ([]byte, error) {
	out := make([]byte, 0, len(data)+len(data)/4)
	start := 0
	lineNo := 0
	for i := 0; i < len(data); i++ {
		if data[i] != '\n' {
			continue
		}
		line := data[start:i]
		if lineNo%4 == 0 {
			if len(line) == 0 || line[0] != '@' {
				return nil, errBadHeader
			}
			*f = fields{}
			if err := parseSurfSeq(line, f); err != nil {
				return nil, fmt.Errorf("%w: %.80s", err, line)
			}
			fcCode, err := enc.encode(f.fc)
			if err != nil {
				return nil, fmt.Errorf("%w: %.80s", err, line)
			}
			out = formatHeader(out, f, p.Opt.To, fcCode)
			out = append(out, '\n')
		} else {
			out = append(out, line...)
			out = append(out, '\n')
		}
		lineNo++
		start = i + 1
	}
	if lineNo%4 != 0 {
		return nil, fmt.Errorf("incomplete record at chunk boundary")
	}
	return out, nil
}

// validateFirst 校验首个 header
func (p *Pipeline) validateFirst(line []byte) error {
	trimmed := bytes.TrimRight(line, "\r\n")
	if len(trimmed) == 0 || trimmed[0] != '@' {
		return fmt.Errorf("first record header missing '@': %.60s", trimmed)
	}
	if plat := detectPlatform(trimmed); plat != SurfSeq {
		return fmt.Errorf("input is %s, not surfseq (first header: %.60s)\n"+
			"surfconv only converts surfseq FASTQ to mgi/illumina", platformName(plat), trimmed)
	}
	var f fields
	if err := parseSurfSeq(trimmed, &f); err != nil {
		return fmt.Errorf("first header: %w: %.60s", err, trimmed)
	}
	if _, err := encodeFC(f.fc); err != nil {
		return fmt.Errorf("first header: %w", err)
	}
	p.FirstFC = f.fc
	return nil
}

func CheckPairs(p1, p2 string) error {
	in1, err := openInput(p1)
	if err != nil {
		return err
	}
	defer in1.Close()
	in2, err := openInput(p2)
	if err != nil {
		return err
	}
	defer in2.Close()
	br1 := bufio.NewReaderSize(in1, 1<<20)
	br2 := bufio.NewReaderSize(in2, 1<<20)
	n := 0
	for {
		h1, err1 := readHeaderLine(br1)
		h2, err2 := readHeaderLine(br2)
		if err1 == io.EOF && err2 == io.EOF {
			return nil
		}
		if err1 != nil || err2 != nil {
			return fmt.Errorf("record %d: length mismatch or read error (%v / %v)", n+1, err1, err2)
		}
		n++
		k1 := pairKey(h1)
		k2 := pairKey(h2)
		if !bytes.Equal(k1, k2) {
			return fmt.Errorf("record %d unpaired:\n  R1 %s\n  R2 %s", n, h1, h2)
		}
		for i := 0; i < 3; i++ {
			if _, err := br1.ReadBytes('\n'); err != nil {
				return fmt.Errorf("record %d R1 truncated", n)
			}
			if _, err := br2.ReadBytes('\n'); err != nil {
				return fmt.Errorf("record %d R2 truncated", n)
			}
		}
	}
}

func readHeaderLine(br *bufio.Reader) ([]byte, error) {
	line, err := br.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	return bytes.TrimRight(line, "\r\n"), nil
}

func pairKey(h []byte) []byte {
	s := h
	if len(s) > 0 && s[0] == '@' {
		s = s[1:]
	}
	if sp := bytes.IndexByte(s, ' '); sp >= 0 {
		second := s[sp+1:]
		ci := bytes.IndexByte(second, ':')
		if ci > 0 {
			return append(append([]byte{}, s[:sp]...), second[ci:]...)
		}
		return append([]byte{}, s[:sp]...)
	}
	if sl := bytes.LastIndexByte(s, '/'); sl >= 0 {
		return append([]byte{}, s[:sl]...)
	}
	return append([]byte{}, s...)
}
