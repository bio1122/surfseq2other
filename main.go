package main

import (
	"flag"
	"fmt"
	"os"
	"sync"
)

func usage() {
	fmt.Fprintf(os.Stderr, `surfconv - SurfSeq FASTQ header converter to MGI / Illumina

Input must be SurfSeq FASTQ; the first header is auto-checked and
non-SurfSeq input is rejected.

Flowcell IDs are compacted to a readable 10-char code (slot letter +
YYMMDD + batch, e.g. A260515156); the original-to-code mapping is
written to <out1>.chipmap.tsv next to the output (first header's FC).

Flowcell letter patterns must be registered in fcCodec.go (fcPatterns);
unknown patterns fail with an explicit error before conversion starts.

Usage:
  surfconv -in1 R1.fq.gz [-in2 R2.fq.gz] -out1 O1.fq.gz [-out2 O2.fq.gz] -to mgi|illumina [options]

Options:
`)
	flag.PrintDefaults()
}

func main() {
	in1 := flag.String("in1", "", "read1 input fastq[.gz] (required)")
	in2 := flag.String("in2", "", "read2 input fastq[.gz] (optional, PE)")
	out1 := flag.String("out1", "", "read1 output fastq[.gz] (required)")
	out2 := flag.String("out2", "", "read2 output fastq[.gz] (required when -in2 given)")
	to := flag.String("to", "", "target platform: mgi|illumina (required)")
	threads := flag.Int("t", 8, "total worker threads")
	zl := flag.Int("zl", 6, "gzip compression level 1-9")
	chunkN := flag.Int("chunk", 8192, "records per chunk")
	checkPair := flag.Bool("check-pair", false, "verify R1/R2 pairing after conversion")
	flag.Usage = usage
	flag.Parse()

	if *in1 == "" || *out1 == "" || *to == "" {
		usage()
		os.Exit(2)
	}
	if *in2 != "" && *out2 == "" {
		fmt.Fprintln(os.Stderr, "error: -out2 required when -in2 given")
		os.Exit(2)
	}

	toP, err := parsePlatform(*to)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	opt := &Options{To: toP}

	jobs := [][2]string{{*in1, *out1}}
	if *in2 != "" {
		jobs = append(jobs, [2]string{*in2, *out2})
	}
	workersPer := *threads / len(jobs)
	if workersPer < 1 {
		workersPer = 1
	}

	var wg sync.WaitGroup
	errs := make([]error, len(jobs))
	pipelines := make([]*Pipeline, len(jobs))
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, in, out string) {
			defer wg.Done()
			pl := &Pipeline{Opt: opt, Workers: workersPer, ChunkN: *chunkN}
			pipelines[i] = pl
			errs[i] = pl.Convert(in, out, *zl)
		}(i, j[0], j[1])
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			fmt.Fprintln(os.Stderr, "error:", e)
			os.Exit(1)
		}
	}
	fmt.Fprintln(os.Stderr, "[surfconv] source platform: surfseq")

	if *checkPair && *in2 != "" {
		if err := CheckPairs(*out1, *out2); err != nil {
			fmt.Fprintln(os.Stderr, "pair check failed:", err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "[surfconv] pair check passed")
	}

	if fc := pipelines[0].FirstFC; fc != "" && *out1 != "-" && *out1 != "/dev/stdout" {
		line, err := fcMapLine(fc)
		if err != nil {
			fmt.Fprintln(os.Stderr, "warning: build chip map failed:", err)
		} else {
			mapPath := chipMapPath(*out1)
			content := "# original_flowcell\tcompact_code\n" + line + "\n"
			if err := os.WriteFile(mapPath, []byte(content), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, "warning: write chip map failed:", err)
			} else {
				fmt.Fprintf(os.Stderr, "[surfconv] chip map written: %s\n", mapPath)
			}
		}
	}
	fmt.Fprintln(os.Stderr, "[surfconv] done")
}

func parsePlatform(s string) (Platform, error) {
	switch s {
	case "mgi":
		return MGI, nil
	case "illumina":
		return Illumina, nil
	}
	return 0, fmt.Errorf("unknown target platform: %s (expected mgi|illumina)", s)
}

func platformName(p Platform) string {
	switch p {
	case MGI:
		return "mgi"
	case Illumina:
		return "illumina"
	case SurfSeq:
		return "surfseq"
	}
	return "unknown"
}
