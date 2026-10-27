# surfconv

Converts **SurfSeq** FASTQ headers to **MGI** or **Illumina** format. Only the header line is modified; sequence, `+`, and quality lines are passed through unchanged.

- Supports paired-end (PE) dual files, multi-threading, and streaming gzip read/write; output order strictly matches input
- Compresses the 21-character SurfSeq flowcell ID into a readable 10-character code, making output headers the same length as native MGI / Illumina
- After a successful conversion, a `.chipmap.tsv` is generated automatically, recording the mapping between original flowcell IDs and compressed codes
- If the input is not SurfSeq, the tool exits with an error before writing any output

## 1. Quick Start

Requires Go 1.22+ and depends on `klauspost/pgzip`.

```bash
go build -o surfconv .
```

Produces a single binary that runs on any Linux x86_64 machine.

```bash
# SurfSeq → MGI (PE)
./surfconv \
  -in1 XJ2DNA_R1.fastq.gz -in2 XJ2DNA_R2.fastq.gz \
  -out1 XJ2DNA_mgi_R1.fastq.gz -out2 XJ2DNA_mgi_R2.fastq.gz \
  -to mgi -t 32 -check-pair

# SurfSeq → Illumina: just change -to to illumina
```

`-to` is required (`mgi` / `illumina`). The input platform is detected and validated automatically, so it does not need to be specified. Other parameters: `-t` thread count, `-zl` compression level (1-9, default 6), `-chunk` records per chunk, `-check-pair` verify R1/R2 pairing after conversion. See `surfconv -h` for the full list.

Both input and output can use `-` for standard streams; when writing to stdout, no chipmap is generated. `.gz` inputs are detected automatically, and whether output is compressed is determined by whether `-out` ends with `.gz`.

On success, stderr prints:

```
[surfconv] source platform: surfseq
[surfconv] pair check passed
[surfconv] chip map written: XJ2DNA_mgi_R1.fastq.chipmap.tsv
[surfconv] done
```

## 2. Flowcell ID Compression

A real MGI flowcell ID is 10 characters (1 letter + 9 digits, e.g. `E250171980`), while a SurfSeq one is 21 characters. Keeping it as-is would make the MGI output header longer than the native one, so it is compressed into a structurally equivalent 10-character code.

### 2.1 SurfSeq Flowcell ID Structure

```
4P 260515 156 US 293243 A2
model  date  batch  origin  serial  slot/version
      YYMMDD  NNN
```

### 2.2 Encoding Rules

```
output = slot letter + YYMMDD + NNN

4P260545156US292743A2 → A260515156
4P250509179US293237D2 → D250509179
```

Each position is validated before encoding; invalid IDs are rejected outright:

| Index | Field | Requirement |
|---|---|---|
| 0-1 | model | alphanumeric |
| 2-7 | date | digits, and valid `YYMMDD` (month 01-12, day 01-31) |
| 8-10 | batch | digits |
| 11-12 | origin | alphanumeric |
| 13-18 | serial | digits |
| 19-20 | slot/version | alphanumeric |

An ID whose length is not 21 is also rejected.

### 2.3 chipmap Lookup Table

The encoding is irreversible; when you need the original flowcell ID, consult `<out1>.chipmap.tsv` (the `-out1` name with `.gz` removed and `.chipmap.tsv` appended):

```
# original_flowcell	compact_code
4P260545156US292743A2	A260515156
```

## 3. Header Conversion Rules

Input example:

```
@AE01-231101003:9:4P260545156US292743A2:L03:R001C003:0000:0132 1:N:0:TAACCTGC+AGTGGTAG
```

### 3.1 → MGI

```
@A260515156L3C003R00100000132/1
```

| SurfSeq | Rule | MGI |
|---|---|---|
| flowcell ID | compress (§2) | `A260515156` |
| `L03` | drop leading zero | `L3` |
| `C003` / `R001` | keep as-is | `C003` / `R001` |
| `0000` + `0132` | 4+4 string concatenation | `00000132` (coord) |
| read number after the space | add `/` prefix | `/1` |
| instrument, run, index, filtered, control | discard | — |

coord uses string concatenation rather than numeric addition: `sub=1000, clust=0132` yields `10000132`, each field taking 4 digits, so there is no overflow or field bleed. This format is based on observed new-version MGI output (Row fixed at 3 digits, coord fixed at 8 digits) and is compatible with the BBTools `BGIHeaderParser2`.

### 3.2 → Illumina

```
@AE01-231101003:9:A260515156:3:1003:0000:0132 1:N:0:TAACCTGC+AGTGGTAG
```

| SurfSeq | Rule | Illumina |
|---|---|---|
| instrument / run | keep as-is | same |
| flowcell ID | compress (§2) | `A260515156` |
| `L03` | drop `L` and leading zero | `3` |
| `R001C003` | tile = R×1000+C, zero-pad to 4 digits | `1003` |
| `0000` / `0132` | zero-pad to 4 digits | X / Y |
| everything after the space | keep as-is | same |

The `R*1000+C` rule for tile is a convention. If the official spec differs, edit `formatHeader` in `header.go`.

### 3.3 Fixed Width

Within a single file, the instrument, flowcell code, and index length are constant, so the output header is fully fixed-length: MGI is always 31 characters, Illumina always 69 characters (measured across a full XJ2DNA run).

| Field | SurfSeq | MGI | Illumina |
|---|---|---|---|
| Flowcell | 21 | 10 | 10 |
| Lane | `L03` (2) | `L3` (1) | `3` (1) |
| Row / Col | 3 / 3 | 3 / 3 | merged into tile (4) |
| Sub / Cluster | 4 / 4 | merged into coord (8) | X / Y (4 each) |

The zero-padding function only pads, never truncates, so fixed length relies on these preconditions: row, col ≤ 3 digits; sub, clust ≤ 4 digits; lane output ≤ 1 digit. Values beyond this (e.g. `C1003`) are written as-is, wider, breaking the fixed length — such input is malformed.

## 4. Verifying Results

```bash
# 1) header fixed length: should print a single value (MGI 31, Illumina 69)
gzip -cd out_R1.fq.gz | awk 'NR%4==1{print length($0)}' | sort -u

# 2) read count matches input
echo $(( $(gzip -cd in_R1.fq.gz | wc -l) / 4 )) $(( $(gzip -cd out_R1.fq.gz | wc -l) / 4 ))

# 3) sequence, +, and quality lines unchanged (should print nothing)
diff <(gzip -cd in_R1.fq.gz  | awk 'NR%4!=1') \
     <(gzip -cd out_R1.fq.gz | awk 'NR%4!=1')

# 4) pairing: run with -check-pair and confirm "pair check passed" appears
# 5) traceability: cat out_R1.fastq.chipmap.tsv
```

## 5. Limitations and Notes

1. Interleaved single-file PE is not supported. For PE, it is recommended to always use `-check-pair`.
2. Converting to MGI is lossy: instrument, run, index, filtered, and control are dropped; positioning fields are lossless, and the flowcell ID can be recovered via the chipmap.
3. The flowcell ID must have the five-segment structure with a registered slot combination, otherwise it errors before writing any data, outputting 0 lines.
4. Flowcell uniqueness relies on "date + batch". If two flowcells with the same date and batch but different serial numbers appear in the future, the codes will collide; this can be detected via the chipmap.
5. The index field must not contain `:`. Illumina dual indices joined by `+` are unaffected.
6. If the input is not the standard 4-line structure or header parsing fails, the tool aborts with an error rather than silently truncating.
7. stdin is processed in a single streaming pass; platform detection, flowcell ID validation, and chipmap collection all happen within the conversion pipeline, without re-reading the stream.

Performance: the bottleneck is gzip; header conversion takes about 100ns per record. With `-t 8`, 1 million PE read pairs take roughly 4 seconds. Be mindful of memory when `-t` is very high — pgzip's memory usage scales with the number of workers.

---

## 6. Implementation Notes

**Parsing** (`header.go`, `parseSurfSeq`): Splits at the first space into two parts; the first part has at least 7 `:`-separated fields, the second at least 4; lane, row, col, sub, cluster, and read must be numeric, and anything after the 4th field is discarded. If any step fails, it reports `unrecognized header` and aborts. The intermediate representation keeps the `(row, col, sub, clust)` tuple; each output direction expands it separately rather than merging and then re-splitting.

**Pipeline**: One per read (R1/R2), with threads split evenly. The reader splits into chunks by `-chunk` and numbers them; W workers only rewrite the header line; the writer reorders by number before output, so output order matches input. Workers share no state, and each caches the flowcell ID encoding, encoding it only once per file. The slice returned by `encode` is reused, so callers must copy it immediately.

**Error handling**: The first header is validated before any data is written (`validateFirst`), so a wrong platform or an unregistered flowcell ID never produces a partial output. On error, all channels are drained to avoid stuck goroutines; error precedence is worker > reader > writer, and a failure on either R1 or R2 results in `exit 1`. The original flowcell ID is passed to main via `Pipeline.FirstFC` to generate the chipmap, without re-reading the input.

**Platform detection** (`detectPlatform`): used only as an input gate. No space means MGI; if the 4th `:` field before the space starts with `L` and is longer than 1 character, it is SurfSeq; otherwise Illumina. Non-SurfSeq input exits with an error.

**IO**: The input is peeked 2 bytes first; if it matches the gzip magic (`1f 8b`), pgzip decompresses it. Output uses pgzip or a 1MB bufio. `multiCloser` closes each layer and propagates errors, avoiding silent truncation caused by a lost gz footer.

**Pair checking** (`CheckPairs`): After conversion, both outputs are reopened and each record's header (with the read number removed — for MGI strip `/1`, `/2`; otherwise the first field after the space) is compared. A mismatch reports `record N unpaired`; if one side ends early, a length mismatch is reported.

## 7. Changelog

- **v1.0** Initial release: SurfSeq → MGI / Illumina header conversion, readable flowcell ID compression with chipmap, fail-fast on non-SurfSeq input, PE parallelism with order preservation, optional pair checking.
